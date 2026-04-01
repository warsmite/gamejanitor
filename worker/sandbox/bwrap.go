package sandbox

import (
	"os"
	"path/filepath"
	"strings"
)

// buildBwrapArgs constructs bubblewrap arguments for full sandbox isolation.
// Mounts the OCI rootfs as /, binds the volume to /data, adds PID namespace isolation.
func buildBwrapArgs(rootFS string, manifest instanceManifest, imgCfg *imageConfig, dataDir string) []string {
	args := []string{
		"--bind", rootFS, "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--tmpfs", "/home",
		"--unshare-pid",
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

	args = append(args, "--setenv", "HOME", "/tmp")

	return args
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
