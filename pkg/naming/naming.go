package naming

import "strings"

const (
	ContainerPrefix        = "gamejanitor-"
	UpdateContainerPrefix  = ContainerPrefix + "update-"
	FileopsContainerPrefix = ContainerPrefix + "fileops-"
)

func InstanceName(gameserverID string) string {
	return ContainerPrefix + gameserverID
}

func UpdateInstanceName(gameserverID string) string {
	return UpdateContainerPrefix + gameserverID
}

func VolumeName(gameserverID string) string {
	return ContainerPrefix + gameserverID
}

// GameserverIDFromInstanceName extracts a gameserver ID from a container name.
// Returns empty string and false for non-gameserver containers (fileops, update, etc).
func GameserverIDFromInstanceName(name string) (string, bool) {
	if !strings.HasPrefix(name, ContainerPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, ContainerPrefix)
	if strings.HasPrefix(id, "update-") || strings.HasPrefix(id, "reinstall-") ||
		strings.HasPrefix(id, "backup-") || strings.HasPrefix(id, "fileops-") {
		return "", false
	}
	return id, true
}
