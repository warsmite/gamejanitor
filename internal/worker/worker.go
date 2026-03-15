package worker

import (
	"context"
	"io"
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

	// Copy operations (used by backup/restore and file ops via container)
	CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error)
	CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error
	CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error)
	CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error

	// Events
	WatchEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error)
}
