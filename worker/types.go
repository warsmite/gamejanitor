package worker

import "time"

// DepotResult contains information about a completed depot download.
type DepotResult struct {
	DepotDir        string
	Cached          bool   // true if depot was already up-to-date (no download)
	BytesDownloaded uint64 // 0 if cached
}

// DepotProgress reports download progress during EnsureDepot.
type DepotProgress struct {
	CompletedBytes  uint64
	TotalBytes      uint64
	CompletedChunks int
	TotalChunks     int
}

type InstanceOptions struct {
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
	Binds         []string // Host bind mounts in "host:instance:opts" format
}

type PortBinding struct {
	HostPort      int
	InstancePort int
	Protocol      string // "tcp" or "udp"
}

type InstanceInfo struct {
	ID        string
	State     string // "running", "exited", etc.
	StartedAt time.Time
	ExitCode  int
}

type InstanceStats struct {
	MemoryUsageMB int
	MemoryLimitMB int
	CPUPercent    float64
	NetRxBytes    int64
	NetTxBytes    int64
}

type GameserverStats struct {
	MemoryUsageMB   int
	MemoryLimitMB   int
	CPUPercent      float64
	NetRxBytes      int64
	NetTxBytes      int64
	VolumeSizeBytes int64
	StorageLimitMB  *int
}

type InstanceEvent struct {
	InstanceID   string
	InstanceName string
	Action        string // "start", "stop", "die", "kill", etc.
}

type GameserverInstance struct {
	InstanceID   string
	InstanceName string
	GameserverID  string // extracted from instance name
	State         string // running, exited, etc.
}

type FileEntry struct {
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	Permissions string    `json:"permissions"`
}
