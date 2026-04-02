package worker

import (
	"context"
	"io"
	"os"
)

// Worker abstracts all instance and host operations.
// LocalWorker implements this via Docker, RemoteWorker via gRPC to a worker agent.
type Worker interface {
	// Instance lifecycle
	PullImage(ctx context.Context, image string) error
	CreateInstance(ctx context.Context, opts InstanceOptions) (string, error)
	StartInstance(ctx context.Context, id string, readyPattern string) error
	StopInstance(ctx context.Context, id string, timeoutSeconds int) error
	RemoveInstance(ctx context.Context, id string) error
	InspectInstance(ctx context.Context, id string) (*InstanceInfo, error)
	Exec(ctx context.Context, instanceID string, cmd []string) (exitCode int, stdout string, stderr string, err error)
	InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error)
	InstanceStats(ctx context.Context, instanceID string) (*InstanceStats, error)

	// Volumes
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeSize(ctx context.Context, volumeName string) (int64, error)

	// Volume file operations (direct filesystem access)
	ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error)
	OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error)
	WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error
	WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error
	DeletePath(ctx context.Context, volumeName string, path string) error
	CreateDirectory(ctx context.Context, volumeName string, path string) error
	RenamePath(ctx context.Context, volumeName string, from string, to string) error
	DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error

	// Copy operations (used by config file read/write)
	CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error)
	CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error
	CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error)
	CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error

	// Volume-level backup operations (instance-independent)
	BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error)
	RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error

	// Discovery
	ListGameserverInstances(ctx context.Context) ([]GameserverInstance, error)

	// Instance state — authoritative state from worker
	WatchInstanceStates(ctx context.Context) (<-chan InstanceStateUpdate, <-chan error)
	GetAllInstanceStates(ctx context.Context) ([]InstanceStateUpdate, error)

	// Game scripts — extract to local filesystem, return host paths for bind-mounts
	PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (scriptDir string, defaultsDir string, err error)

	// Copy depot files into a volume's /server directory on the host.
	// Done outside the instance to avoid cgroup OOM on large depots.
	CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error

	// Steam depot — download game files to local cache, return host path and download info.
	// onProgress is called during download with progress updates. May be nil.
	EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(DepotProgress)) (*DepotResult, error)

	// Steam Workshop — download a UGC item to a volume path.
	DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error
}
