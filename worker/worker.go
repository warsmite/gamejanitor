package worker

import (
	"context"
	"io"
	"os"
)

// Worker is the transport-agnostic abstraction over a node that runs gameserver
// instances. LocalWorker satisfies it directly; RemoteWorker satisfies it over
// gRPC. The interface is composed of focused sub-interfaces so consumers can
// depend on the narrowest surface they need (least-privilege).
type Worker interface {
	InstanceManager
	VolumeManager
	FileManager
	ScriptManager
	DepotManager
	StateWatcher
}

// InstanceManager owns the lifecycle of a single instance and its image.
type InstanceManager interface {
	PullImage(ctx context.Context, image string, onProgress func(PullProgress)) error
	CreateInstance(ctx context.Context, opts InstanceOptions) (string, error)
	StartInstance(ctx context.Context, id string, readyPattern string) error
	StopInstance(ctx context.Context, id string, timeoutSeconds int) error
	RemoveInstance(ctx context.Context, id string) error
	InspectInstance(ctx context.Context, id string) (*InstanceInfo, error)
	Exec(ctx context.Context, instanceID string, cmd []string) (exitCode int, stdout string, stderr string, err error)
	InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error)
	InstanceStats(ctx context.Context, instanceID string) (*InstanceStats, error)
	ListGameserverInstances(ctx context.Context) ([]GameserverInstance, error)
}

// VolumeManager owns persistent storage attached to instances.
type VolumeManager interface {
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeSize(ctx context.Context, volumeName string) (int64, error)
	BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error)
	RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error
}

// FileManager covers direct filesystem access to volumes plus copy in/out of
// running instances (used by config file editing and similar flows).
type FileManager interface {
	ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error)
	OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error)
	WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error
	WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error
	DeletePath(ctx context.Context, volumeName string, path string) error
	CreateDirectory(ctx context.Context, volumeName string, path string) error
	RenamePath(ctx context.Context, volumeName string, from string, to string) error
	DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error

	CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error)
	CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error
	CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error)
	CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error
}

// ScriptManager extracts game scripts to the worker filesystem so they can be
// bind-mounted into instances.
type ScriptManager interface {
	PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (scriptDir string, defaultsDir string, err error)
}

// DepotManager handles Steam-specific game file delivery (depots and Workshop
// items). Depots are downloaded host-side and copied into volumes outside the
// container to avoid cgroup OOM on large depots.
type DepotManager interface {
	CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error
	EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(DepotProgress)) (*DepotResult, error)
	DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error
}

// StateWatcher exposes authoritative process state from the worker. The
// controller subscribes to WatchInstanceStates for live updates and uses
// GetAllInstanceStates for full reconciliation on startup.
type StateWatcher interface {
	WatchInstanceStates(ctx context.Context) (<-chan InstanceStateUpdate, <-chan error)
	GetAllInstanceStates(ctx context.Context) ([]InstanceStateUpdate, error)
}
