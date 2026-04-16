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

type PullProgress struct {
	CompletedBytes  uint64
	TotalBytes      uint64
	CompletedLayers int
	TotalLayers     int
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
	Port          int    // host-side port (allocated by scheduler)
	ContainerPort int    // container-side port (game default, what the process binds)
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
}

type GameserverStats struct {
	MemoryUsageMB   int
	MemoryLimitMB   int
	CPUPercent      float64
	VolumeSizeBytes int64
	StorageLimitMB  *int
}

// InstanceState is the process lifecycle on the worker. It is orthogonal to
// readiness: a Running process may not yet be ready, and becomes ready
// separately when the ready pattern matches (or immediately if no pattern).
type InstanceState int

const (
	StateCreated InstanceState = 0 // container created, process not launched
	StateRunning InstanceState = 1 // process is alive
	StateExited  InstanceState = 2 // process terminated
)

func (s InstanceState) String() string {
	switch s {
	case StateCreated:
		return "created"
	case StateRunning:
		return "running"
	case StateExited:
		return "exited"
	default:
		return "unknown"
	}
}

type InstanceStateUpdate struct {
	InstanceID   string
	InstanceName string
	State        InstanceState
	Ready        bool      // true when ready pattern matched (or on launch with no pattern)
	ReadyAt      time.Time // time Ready transitioned to true; zero if not ready
	ExitCode     int
	StartedAt    time.Time
	ExitedAt     time.Time
	Installed    bool
}

type GameserverInstance struct {
	InstanceID   string
	InstanceName string
	GameserverID string // extracted from instance name
	State        string // running, exited, etc.
}

type FileEntry struct {
	Name        string    `json:"name"`
	IsDir       bool      `json:"is_dir"`
	Size        int64     `json:"size"`
	ModTime     time.Time `json:"mod_time"`
	Permissions string    `json:"permissions"`
}
