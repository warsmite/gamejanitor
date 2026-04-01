package sandbox

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// buildBwrapArgs constructs bubblewrap arguments for full sandbox isolation.
// Mounts the OCI rootfs as /, binds the volume to /data, adds PID namespace isolation.
func buildBwrapArgs(rootFS string, manifest instanceManifest, imgCfg *imageConfig, dataDir string) []string {
	uid, gid := parseImageUser(imgCfg.User, rootFS)

	args := []string{
		"--ro-bind", rootFS, "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home",
		"--tmpfs", "/run",
		"--tmpfs", "/var/tmp",
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-uts",
		"--unshare-cgroup",
		"--new-session",
		"--uid", strconv.Itoa(uid),
		"--gid", strconv.Itoa(gid),
	}

	// Bind DNS config
	resolvConf := filepath.Join(dataDir, "resolv.conf")
	if _, err := os.Stat(resolvConf); err == nil {
		args = append(args, "--ro-bind", resolvConf, "/etc/resolv.conf")
	} else if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}

	// Bind host SSL certs if the rootfs doesn't have its own
	rootFSCerts := filepath.Join(rootFS, "etc/ssl/certs/ca-certificates.crt")
	if _, err := os.Stat(rootFSCerts); err != nil {
		for _, certPath := range []struct{ src, dst string }{
			{"/etc/ssl/certs", "/etc/ssl/certs"},
			{"/etc/pki/tls/certs", "/etc/pki/tls/certs"},
		} {
			if _, err := os.Stat(certPath.src); err == nil {
				args = append(args, "--ro-bind", certPath.src, certPath.dst)
			}
		}
	}

	// Bind volume to /data
	if manifest.VolumeName != "" {
		volumeDir := filepath.Join(dataDir, "volumes", manifest.VolumeName)
		args = append(args, "--bind", volumeDir, "/data")
	}

	// Bind-mount host paths (scripts, defaults, depot)
	for _, bind := range manifest.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) >= 2 {
			hostPath := parts[0]
			instancePath := parts[1]
			if len(parts) == 3 && strings.Contains(parts[2], "ro") {
				args = append(args, "--ro-bind", hostPath, instancePath)
			} else {
				args = append(args, "--bind", hostPath, instancePath)
			}
		}
	}

	// Working directory
	if imgCfg.WorkingDir != "" {
		args = append(args, "--chdir", imgCfg.WorkingDir)
	} else {
		args = append(args, "--chdir", "/data")
	}

	// Environment variables from image config
	for _, e := range imgCfg.Env {
		args = append(args, "--setenv", envKey(e), envVal(e))
	}
	// Environment variables from manifest (user overrides)
	for _, e := range manifest.Env {
		args = append(args, "--setenv", envKey(e), envVal(e))
	}

	return args
}

// parseImageUser parses the OCI User field into uid and gid.
// Accepts "uid", "uid:gid", "username", or "username:group".
// Resolves usernames from /etc/passwd in the rootfs.
// Returns 0,0 (root) if empty or unresolvable.
func parseImageUser(user string, rootFS string) (int, int) {
	if user == "" {
		return 0, 0
	}
	parts := strings.SplitN(user, ":", 2)

	uid, uidErr := strconv.Atoi(parts[0])
	if uidErr != nil {
		// Username — resolve from rootfs /etc/passwd
		uid = lookupUIDInPasswd(filepath.Join(rootFS, "etc/passwd"), parts[0])
	}

	gid := uid
	if len(parts) == 2 {
		if g, err := strconv.Atoi(parts[1]); err == nil {
			gid = g
		} else {
			gid = lookupGIDInGroup(filepath.Join(rootFS, "etc/group"), parts[1])
		}
	} else if uidErr != nil {
		// Username with no group — look up primary GID from passwd
		gid = lookupGIDForUser(filepath.Join(rootFS, "etc/passwd"), parts[0])
	}

	return uid, gid
}

// lookupUIDInPasswd finds a UID by username in an /etc/passwd file.
func lookupUIDInPasswd(passwdPath, username string) int {
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[0] == username {
			uid, _ := strconv.Atoi(fields[2])
			return uid
		}
	}
	return 0
}

// lookupGIDForUser finds the primary GID for a username in /etc/passwd.
func lookupGIDForUser(passwdPath, username string) int {
	data, err := os.ReadFile(passwdPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 4 && fields[0] == username {
			gid, _ := strconv.Atoi(fields[3])
			return gid
		}
	}
	return 0
}

// lookupGIDInGroup finds a GID by group name in an /etc/group file.
func lookupGIDInGroup(groupPath, groupname string) int {
	data, err := os.ReadFile(groupPath)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 3 && fields[0] == groupname {
			gid, _ := strconv.Atoi(fields[2])
			return gid
		}
	}
	return 0
}

func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

func envVal(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[i+1:]
	}
	return ""
}
