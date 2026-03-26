package local

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/docker"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// LocalWorker implements Worker by delegating to the Docker client.
// Used in standalone mode where controller and worker run in the same process.
type LocalWorker struct {
	Docker    *docker.Client
	Log       *slog.Logger
	GameStore *games.GameStore
	DataDir   string
	Resolve   worker.VolumeResolver

	// Direct volume access detection (probed once on first file op)
	DirectAccessOnce sync.Once
	DirectAccess     bool

	// Lazy sidecar containers for when direct volume access is unavailable
	SidecarMu    sync.Mutex
	SidecarCache map[string]string // volume name -> container ID

	// Volume size cache (120s staleness)
	VolumeSizeMu    sync.Mutex
	VolumeSizeCache map[string]*VolumeSizeEntry
}

type VolumeSizeEntry struct {
	SizeBytes  int64
	MeasuredAt time.Time
}

func New(dockerClient *docker.Client, gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	w := &LocalWorker{
		Docker:          dockerClient,
		Log:             log,
		GameStore:       gameStore,
		DataDir:         dataDir,
		SidecarCache:    make(map[string]string),
		VolumeSizeCache: make(map[string]*VolumeSizeEntry),
	}
	w.Resolve = w.DockerVolumeResolver()
	return w
}

func (w *LocalWorker) PullImage(ctx context.Context, image string) error {
	return w.Docker.PullImage(ctx, image)
}

func (w *LocalWorker) CreateContainer(ctx context.Context, opts worker.ContainerOptions) (string, error) {
	return w.Docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:          opts.Name,
		Image:         opts.Image,
		Env:           opts.Env,
		Ports:         toDockerPorts(opts.Ports),
		VolumeName:    opts.VolumeName,
		MemoryLimitMB: opts.MemoryLimitMB,
		CPULimit:      opts.CPULimit,
		CPUEnforced:   opts.CPUEnforced,
		Entrypoint:    opts.Entrypoint,
		User:          opts.User,
		Binds:         opts.Binds,
	})
}

func (w *LocalWorker) StartContainer(ctx context.Context, id string) error {
	return w.Docker.StartContainer(ctx, id)
}

func (w *LocalWorker) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	return w.Docker.StopContainer(ctx, id, timeoutSeconds)
}

func (w *LocalWorker) RemoveContainer(ctx context.Context, id string) error {
	return w.Docker.RemoveContainer(ctx, id)
}

func (w *LocalWorker) InspectContainer(ctx context.Context, id string) (*worker.ContainerInfo, error) {
	info, err := w.Docker.InspectContainer(ctx, id)
	if err != nil {
		return nil, err
	}
	return &worker.ContainerInfo{
		ID:        info.ID,
		State:     info.State,
		StartedAt: info.StartedAt,
		ExitCode:  info.ExitCode,
	}, nil
}

func (w *LocalWorker) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	return w.Docker.Exec(ctx, containerID, cmd)
}

func (w *LocalWorker) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	return w.Docker.ContainerLogs(ctx, containerID, tail, follow)
}

func (w *LocalWorker) ContainerStats(ctx context.Context, containerID string) (*worker.ContainerStats, error) {
	stats, err := w.Docker.ContainerStats(ctx, containerID)
	if err != nil {
		return nil, err
	}
	return &worker.ContainerStats{
		MemoryUsageMB: stats.MemoryUsageMB,
		MemoryLimitMB: stats.MemoryLimitMB,
		CPUPercent:    stats.CPUPercent,
	}, nil
}

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	return w.Docker.CreateVolume(ctx, name)
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	w.VolumeSizeMu.Lock()
	delete(w.VolumeSizeCache, name)
	w.VolumeSizeMu.Unlock()

	w.RemoveSidecar(context.Background(), name)

	return w.Docker.RemoveVolume(ctx, name)
}

func (w *LocalWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	w.VolumeSizeMu.Lock()
	if entry, ok := w.VolumeSizeCache[volumeName]; ok && time.Since(entry.MeasuredAt) < 120*time.Second {
		size := entry.SizeBytes
		w.VolumeSizeMu.Unlock()
		return size, nil
	}
	w.VolumeSizeMu.Unlock()

	// Try direct measurement first, fall back to sidecar
	if w.HasDirectAccess(ctx, volumeName) {
		size, err := worker.VolumeSizeDirect(w.Resolve, ctx, volumeName)
		if err == nil {
			w.VolumeSizeMu.Lock()
			w.VolumeSizeCache[volumeName] = &VolumeSizeEntry{SizeBytes: size, MeasuredAt: time.Now()}
			w.VolumeSizeMu.Unlock()
			return size, nil
		}
	}

	exitCode, stdout, stderr, err := w.SidecarExec(ctx, volumeName, []string{"du", "-sb", "/data"})
	if err != nil {
		return 0, fmt.Errorf("measuring volume size: %w", err)
	}
	if exitCode != 0 {
		return 0, fmt.Errorf("measuring volume size: %s", stderr)
	}

	var sizeBytes int64
	if _, err := fmt.Sscanf(strings.TrimSpace(stdout), "%d", &sizeBytes); err != nil {
		return 0, fmt.Errorf("parsing volume size from %q: %w", stdout, err)
	}

	w.VolumeSizeMu.Lock()
	w.VolumeSizeCache[volumeName] = &VolumeSizeEntry{SizeBytes: sizeBytes, MeasuredAt: time.Now()}
	w.VolumeSizeMu.Unlock()

	return sizeBytes, nil
}

// --- Volume file operations ---
// Uses direct filesystem access when the host volume mountpoints are accessible,
// falls back to a lazy sidecar container when running inside Docker.

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.ListFilesDirect(w.Resolve, ctx, volumeName, path)
	}
	return w.listFilesSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.ReadFileDirect(w.Resolve, ctx, volumeName, path)
	}
	return w.readFileSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.WriteFileDirect(w.Resolve, ctx, volumeName, path, content, perm)
	}
	return w.writeFileSidecar(ctx, volumeName, path, content)
}

func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.DeletePathDirect(w.Resolve, ctx, volumeName, path)
	}
	return w.deletePathSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.CreateDirectoryDirect(w.Resolve, ctx, volumeName, path)
	}
	return w.createDirectorySidecar(ctx, volumeName, path)
}

func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	if w.HasDirectAccess(ctx, volumeName) {
		return worker.RenamePathDirect(w.Resolve, ctx, volumeName, from, to)
	}
	return w.renamePathSidecar(ctx, volumeName, from, to)
}

func (w *LocalWorker) WatchEvents(ctx context.Context) (<-chan worker.ContainerEvent, <-chan error) {
	dockerEventCh, dockerErrCh := w.Docker.WatchEvents(ctx)

	eventCh := make(chan worker.ContainerEvent)
	errCh := make(chan error, 1)

	go func() {
		defer close(eventCh)
		defer close(errCh)

		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-dockerErrCh:
				if !ok {
					return
				}
				errCh <- err
				return
			case de, ok := <-dockerEventCh:
				if !ok {
					return
				}
				select {
				case eventCh <- worker.ContainerEvent{
					ContainerID:   de.ContainerID,
					ContainerName: de.ContainerName,
					Action:        de.Action,
				}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return eventCh, errCh
}

func (w *LocalWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return worker.PrepareGameScripts(w.GameStore, w.DataDir, gameID, gameserverID)
}

func toDockerPorts(ports []worker.PortBinding) []docker.PortBinding {
	out := make([]docker.PortBinding, len(ports))
	for i, p := range ports {
		out[i] = docker.PortBinding{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		}
	}
	return out
}

// --- Copy operations (used by backup/restore) ---

func (w *LocalWorker) CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error) {
	return w.Docker.CopyFromContainer(ctx, containerID, path)
}

func (w *LocalWorker) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	return w.Docker.CopyToContainer(ctx, containerID, path, content)
}

func (w *LocalWorker) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	return w.Docker.CopyDirFromContainer(ctx, containerID, path)
}

func (w *LocalWorker) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	return w.Docker.CopyTarToContainer(ctx, containerID, destPath, content)
}

func (w *LocalWorker) ListGameserverContainers(ctx context.Context) ([]worker.GameserverContainer, error) {
	containers, err := w.Docker.ListGameserverContainers(ctx)
	if err != nil {
		return nil, err
	}
	var result []worker.GameserverContainer
	for _, c := range containers {
		gsID, ok := naming.GameserverIDFromContainerName(c.Name)
		if !ok {
			continue // update/fileops/backup container, not a gameserver
		}
		result = append(result, worker.GameserverContainer{
			ContainerID:   c.ID,
			ContainerName: c.Name,
			GameserverID:  gsID,
			State:         c.State,
		})
	}
	return result, nil
}
