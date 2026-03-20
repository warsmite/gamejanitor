package worker

import (
	"context"
	"io"
	"os"
)

// Worker abstracts all container and host operations.
// LocalWorker implements this for single-node via Docker.
// RemoteWorker (future) will implement this via gRPC to a worker agent.
type Worker interface {
	// Container lifecycle
	PullImage(ctx context.Context, image string) error
	CreateContainer(ctx context.Context, opts ContainerOptions) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string, timeoutSeconds int) error
	RemoveContainer(ctx context.Context, id string) error
	InspectContainer(ctx context.Context, id string) (*ContainerInfo, error)
	Exec(ctx context.Context, containerID string, cmd []string) (exitCode int, stdout string, stderr string, err error)
	ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error)
	ContainerStats(ctx context.Context, containerID string) (*ContainerStats, error)

	// Volumes
	CreateVolume(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeSize(ctx context.Context, volumeName string) (int64, error)

	// Volume file operations (direct filesystem access)
	ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error)
	ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error)
	WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error
	DeletePath(ctx context.Context, volumeName string, path string) error
	CreateDirectory(ctx context.Context, volumeName string, path string) error
	RenamePath(ctx context.Context, volumeName string, from string, to string) error

	// Copy operations (used by config file read/write)
	CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error)
	CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error
	CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error)
	CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error

	// Volume-level backup operations (container-independent)
	BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error)
	RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error

	// Events
	WatchEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error)

	// Game scripts — extract to local filesystem, return host paths for bind-mounts
	PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (scriptDir string, defaultsDir string, err error)
}
