package worker

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
)

// LocalWorker implements Worker by delegating to the Docker client.
// Used in standalone mode where controller and worker run in the same process.
type LocalWorker struct {
	docker    *docker.Client
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string
	resolve   volumeResolver

	// Direct volume access detection (probed once on first file op)
	directAccessOnce sync.Once
	directAccess     bool

	// Lazy sidecar containers for when direct volume access is unavailable
	sidecarMu    sync.Mutex
	sidecarCache map[string]string // volume name → container ID

	// Volume size cache (120s staleness)
	volumeSizeMu    sync.Mutex
	volumeSizeCache map[string]*volumeSizeEntry
}

type volumeSizeEntry struct {
	sizeBytes  int64
	measuredAt time.Time
}

func NewLocalWorker(dockerClient *docker.Client, gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	w := &LocalWorker{
		docker:          dockerClient,
		log:             log,
		gameStore:       gameStore,
		dataDir:         dataDir,
		sidecarCache:    make(map[string]string),
		volumeSizeCache: make(map[string]*volumeSizeEntry),
	}
	w.resolve = w.dockerVolumeResolver()
	return w
}

func (w *LocalWorker) PullImage(ctx context.Context, image string) error {
	return w.docker.PullImage(ctx, image)
}

func (w *LocalWorker) CreateContainer(ctx context.Context, opts ContainerOptions) (string, error) {
	return w.docker.CreateContainer(ctx, docker.ContainerOptions{
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
	return w.docker.StartContainer(ctx, id)
}

func (w *LocalWorker) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	return w.docker.StopContainer(ctx, id, timeoutSeconds)
}

func (w *LocalWorker) RemoveContainer(ctx context.Context, id string) error {
	return w.docker.RemoveContainer(ctx, id)
}

func (w *LocalWorker) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	info, err := w.docker.InspectContainer(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ContainerInfo{
		ID:        info.ID,
		State:     info.State,
		StartedAt: info.StartedAt,
		ExitCode:  info.ExitCode,
	}, nil
}

func (w *LocalWorker) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	return w.docker.Exec(ctx, containerID, cmd)
}

func (w *LocalWorker) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	return w.docker.ContainerLogs(ctx, containerID, tail, follow)
}

func (w *LocalWorker) ContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	stats, err := w.docker.ContainerStats(ctx, containerID)
	if err != nil {
		return nil, err
	}
	return &ContainerStats{
		MemoryUsageMB: stats.MemoryUsageMB,
		MemoryLimitMB: stats.MemoryLimitMB,
		CPUPercent:    stats.CPUPercent,
	}, nil
}

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	return w.docker.CreateVolume(ctx, name)
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	w.volumeSizeMu.Lock()
	delete(w.volumeSizeCache, name)
	w.volumeSizeMu.Unlock()

	w.removeSidecar(context.Background(), name)

	return w.docker.RemoveVolume(ctx, name)
}

func (w *LocalWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	w.volumeSizeMu.Lock()
	if entry, ok := w.volumeSizeCache[volumeName]; ok && time.Since(entry.measuredAt) < 120*time.Second {
		size := entry.sizeBytes
		w.volumeSizeMu.Unlock()
		return size, nil
	}
	w.volumeSizeMu.Unlock()

	// Try direct measurement first, fall back to sidecar
	if w.hasDirectAccess(ctx, volumeName) {
		size, err := volumeSizeDirect(w.resolve, ctx, volumeName)
		if err == nil {
			w.volumeSizeMu.Lock()
			w.volumeSizeCache[volumeName] = &volumeSizeEntry{sizeBytes: size, measuredAt: time.Now()}
			w.volumeSizeMu.Unlock()
			return size, nil
		}
	}

	exitCode, stdout, stderr, err := w.sidecarExec(ctx, volumeName, []string{"du", "-sb", "/data"})
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

	w.volumeSizeMu.Lock()
	w.volumeSizeCache[volumeName] = &volumeSizeEntry{sizeBytes: sizeBytes, measuredAt: time.Now()}
	w.volumeSizeMu.Unlock()

	return sizeBytes, nil
}

// --- Volume file operations ---
// Uses direct filesystem access when the host volume mountpoints are accessible,
// falls back to a lazy sidecar container when running inside Docker.

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return listFilesDirect(w.resolve, ctx, volumeName, path)
	}
	return w.listFilesSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return readFileDirect(w.resolve, ctx, volumeName, path)
	}
	return w.readFileSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return writeFileDirect(w.resolve, ctx, volumeName, path, content, perm)
	}
	return w.writeFileSidecar(ctx, volumeName, path, content)
}

func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return deletePathDirect(w.resolve, ctx, volumeName, path)
	}
	return w.deletePathSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return createDirectoryDirect(w.resolve, ctx, volumeName, path)
	}
	return w.createDirectorySidecar(ctx, volumeName, path)
}

func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return renamePathDirect(w.resolve, ctx, volumeName, from, to)
	}
	return w.renamePathSidecar(ctx, volumeName, from, to)
}

func (w *LocalWorker) WatchEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	dockerEventCh, dockerErrCh := w.docker.WatchEvents(ctx)

	eventCh := make(chan ContainerEvent)
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
				case eventCh <- ContainerEvent{
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
	return prepareGameScripts(w.gameStore, w.dataDir, gameID, gameserverID)
}

func toDockerPorts(ports []PortBinding) []docker.PortBinding {
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
	return w.docker.CopyFromContainer(ctx, containerID, path)
}

func (w *LocalWorker) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	return w.docker.CopyToContainer(ctx, containerID, path, content)
}

func (w *LocalWorker) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	return w.docker.CopyDirFromContainer(ctx, containerID, path)
}

func (w *LocalWorker) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	return w.docker.CopyTarToContainer(ctx, containerID, destPath, content)
}

func (w *LocalWorker) ListGameserverContainers(ctx context.Context) ([]GameserverContainer, error) {
	containers, err := w.docker.ListGameserverContainers(ctx)
	if err != nil {
		return nil, err
	}
	var result []GameserverContainer
	for _, c := range containers {
		gsID, ok := naming.GameserverIDFromContainerName(c.Name)
		if !ok {
			continue // update/fileops/backup container, not a gameserver
		}
		result = append(result, GameserverContainer{
			ContainerID:   c.ID,
			ContainerName: c.Name,
			GameserverID:  gsID,
			State:         c.State,
		})
	}
	return result, nil
}
