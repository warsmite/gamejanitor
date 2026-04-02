package naming

import "strings"

const (
	InstancePrefix         = "gamejanitor-"
	InstallInstancePrefix  = InstancePrefix + "install-"
	UpdateInstancePrefix   = InstancePrefix + "update-"
	FileopsInstancePrefix  = InstancePrefix + "fileops-"
)

func InstanceName(gameserverID string) string {
	return InstancePrefix + gameserverID
}

func InstallInstanceName(gameserverID string) string {
	return InstallInstancePrefix + gameserverID
}

func UpdateInstanceName(gameserverID string) string {
	return UpdateInstancePrefix + gameserverID
}

func VolumeName(gameserverID string) string {
	return InstancePrefix + gameserverID
}

// GameserverIDFromInstanceName extracts a gameserver ID from a instance name.
// Returns empty string and false for non-gameserver instances (fileops, update, etc).
func GameserverIDFromInstanceName(name string) (string, bool) {
	if !strings.HasPrefix(name, InstancePrefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, InstancePrefix)
	if strings.HasPrefix(id, "install-") || strings.HasPrefix(id, "update-") || strings.HasPrefix(id, "reinstall-") ||
		strings.HasPrefix(id, "backup-") || strings.HasPrefix(id, "fileops-") {
		return "", false
	}
	return id, true
}
