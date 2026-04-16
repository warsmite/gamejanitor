package local

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

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
