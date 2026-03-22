package worker

import "time"

type ContainerOptions struct {
	Name          string
	Image         string
	Env           []string // "KEY=VALUE" format
	Ports         []PortBinding
	VolumeName    string
	MemoryLimitMB int
	CPULimit      float64
	CPUEnforced   bool
	Entrypoint    []string // Override image entrypoint (e.g., ["sleep", "infinity"])
	User          string   // Run as specific user (e.g., "1001:1001")
	Binds         []string // Host bind mounts in "host:container:opts" format
}

type PortBinding struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

type ContainerInfo struct {
	ID        string
	State     string // "running", "exited", etc.
	StartedAt time.Time
	ExitCode  int
}

type ContainerStats struct {
	MemoryUsageMB int
	MemoryLimitMB int
	CPUPercent    float64
}

type GameserverStats struct {
	MemoryUsageMB int
	MemoryLimitMB int
	CPUPercent    float64
	VolumeSizeBytes int64
	StorageLimitMB  *int
}

type ContainerEvent struct {
	ContainerID   string
	ContainerName string
	Action        string // "start", "stop", "die", "kill", etc.
}

type FileEntry struct {
	Name        string
	IsDir       bool
	Size        int64
	ModTime     time.Time
	Permissions string
}
