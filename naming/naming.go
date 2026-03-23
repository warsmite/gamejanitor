package naming

import "strings"

const (
	ContainerPrefix        = "gamejanitor-"
	UpdateContainerPrefix  = ContainerPrefix + "update-"
	FileopsContainerPrefix = ContainerPrefix + "fileops-"
)

func ContainerName(gameserverID string) string {
	return ContainerPrefix + gameserverID
}

func UpdateContainerName(gameserverID string) string {
	return UpdateContainerPrefix + gameserverID
}

func FileopsContainerName(volumeName string) string {
	return FileopsContainerPrefix + volumeName
}

func VolumeName(gameserverID string) string {
	return ContainerPrefix + gameserverID
}

// GameserverIDFromContainerName extracts a gameserver ID from a container name.
// Returns empty string and false for non-gameserver containers (fileops, update, etc).
func GameserverIDFromContainerName(name string) (string, bool) {
	if !strings.HasPrefix(name, ContainerPrefix) {
		return "", false
	}
	id := strings.TrimPrefix(name, ContainerPrefix)
	if strings.Contains(id, "-update-") || strings.Contains(id, "-reinstall-") ||
		strings.Contains(id, "-backup-") || strings.Contains(id, "-fileops-") {
		return "", false
	}
	return id, true
}
