package docker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// LocalWorker implements Worker by delegating to the Docker client.
// Used in standalone mode where controller and worker run in the same process.
type LocalWorker struct {
	Docker    *Client
	Log       *slog.Logger
	GameStore *games.GameStore
	DataDir   string
	Resolve   worker.VolumeResolver
	Tracker   *worker.InstanceTracker

	// Volume size cache (120s staleness)
	VolumeSizeMu    sync.Mutex
	VolumeSizeCache map[string]*VolumeSizeEntry
}

type VolumeSizeEntry struct {
	SizeBytes  int64
	MeasuredAt time.Time
}

func NewWorker(dockerClient *Client, gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	w := &LocalWorker{
		Docker:          dockerClient,
		Log:             log,
		GameStore:       gameStore,
		DataDir:         dataDir,
		VolumeSizeCache: make(map[string]*VolumeSizeEntry),
	}
	w.Resolve = w.DockerVolumeResolver()
	return w
}

func (w *LocalWorker) PullImage(ctx context.Context, image string) error {
	return w.Docker.PullImage(ctx, image)
}

func (w *LocalWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
	return w.Docker.CreateInstance(ctx, InstanceOptions{
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

func (w *LocalWorker) StartInstance(ctx context.Context, id string, readyPattern string) error {
	if err := w.Docker.StartInstance(ctx, id); err != nil {
		return err
	}
	if w.Tracker != nil {
		w.Tracker.Track(id, id) // Docker uses container ID as both ID and name
		w.Tracker.SetState(id, worker.StateStarting)
		logReader, err := w.Docker.InstanceLogs(ctx, id, 0, true)
		if err == nil {
			w.Tracker.WatchLogs(context.Background(), id, readyPattern, logReader)
		}
	}
	return nil
}

func (w *LocalWorker) RunInstall(ctx context.Context, id string) (int, string, error) {
	exitCode, stdout, stderr, err := w.Docker.Exec(ctx, id, []string{"/scripts/install-server"})
	if err != nil {
		return -1, "", err
	}
	output := stdout
	if stderr != "" {
		output += "\n" + stderr
	}
	if exitCode == 0 && w.Tracker != nil {
		w.Tracker.SetInstalled(id)
	}
	return exitCode, output, nil
}

func (w *LocalWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	return w.Docker.StopInstance(ctx, id, timeoutSeconds)
}

func (w *LocalWorker) RemoveInstance(ctx context.Context, id string) error {
	if w.Tracker != nil {
		w.Tracker.Remove(id)
	}
	return w.Docker.RemoveInstance(ctx, id)
}

func (w *LocalWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
	info, err := w.Docker.InspectInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	return &worker.InstanceInfo{
		ID:        info.ID,
		State:     info.State,
		StartedAt: info.StartedAt,
		ExitCode:  info.ExitCode,
	}, nil
}

func (w *LocalWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	return w.Docker.Exec(ctx, instanceID, cmd)
}

func (w *LocalWorker) InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error) {
	return w.Docker.InstanceLogs(ctx, instanceID, tail, follow)
}

func (w *LocalWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
	stats, err := w.Docker.InstanceStats(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	return &worker.InstanceStats{
		MemoryUsageMB: stats.MemoryUsageMB,
		MemoryLimitMB: stats.MemoryLimitMB,
		CPUPercent:    stats.CPUPercent,
		NetRxBytes:    stats.NetRxBytes,
		NetTxBytes:    stats.NetTxBytes,
	}, nil
}

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	if err := w.Docker.CreateVolume(ctx, name); err != nil {
		return err
	}
	// Set ownership so the gameserver user (1001) can write before the instance starts.
	// Docker creates volumes as root, but mods may be installed before first start.
	mountpoint, err := w.Resolve(ctx, name)
	if err != nil {
		return fmt.Errorf("resolving volume mountpoint for chown: %w", err)
	}
	if err := os.Chown(mountpoint, model.GameserverUID, model.GameserverGID); err != nil {
		return fmt.Errorf("setting volume ownership: %w", err)
	}
	return nil
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	w.VolumeSizeMu.Lock()
	delete(w.VolumeSizeCache, name)
	w.VolumeSizeMu.Unlock()

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

	size, err := worker.VolumeSizeDirect(w.Resolve, ctx, volumeName)
	if err != nil {
		return 0, fmt.Errorf("measuring volume size: %w", err)
	}

	w.VolumeSizeMu.Lock()
	w.VolumeSizeCache[volumeName] = &VolumeSizeEntry{SizeBytes: size, MeasuredAt: time.Now()}
	w.VolumeSizeMu.Unlock()

	return size, nil
}

// --- Volume file operations (always direct filesystem access) ---

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	return worker.ListFilesDirect(w.Resolve, ctx, volumeName, path)
}

func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	return worker.ReadFileDirect(w.Resolve, ctx, volumeName, path)
}

func (w *LocalWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	return worker.OpenFileDirect(w.Resolve, ctx, volumeName, path)
}

func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	return worker.WriteFileDirect(w.Resolve, ctx, volumeName, path, content, perm)
}

func (w *LocalWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	return worker.WriteFileStreamDirect(w.Resolve, ctx, volumeName, path, reader, perm)
}

func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	return worker.DeletePathDirect(w.Resolve, ctx, volumeName, path)
}

func (w *LocalWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	return worker.DownloadFileDirect(w.Resolve, ctx, volumeName, url, destPath, expectedHash, maxBytes)
}

func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	return worker.CreateDirectoryDirect(w.Resolve, ctx, volumeName, path)
}

func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	return worker.RenamePathDirect(w.Resolve, ctx, volumeName, from, to)
}

func (w *LocalWorker) WatchInstanceStates(ctx context.Context) (<-chan worker.InstanceStateUpdate, <-chan error) {
	errCh := make(chan error, 1)
	if w.Tracker == nil {
		return make(chan worker.InstanceStateUpdate), errCh
	}
	return w.Tracker.Events(), errCh
}

// WatchDockerEvents translates Docker container "die" events into tracker state
// changes. Must be called once when the worker starts (e.g. from serve.go).
func (w *LocalWorker) WatchDockerEvents(ctx context.Context) {
	if w.Tracker == nil {
		return
	}
	eventCh, errCh := w.Docker.WatchEvents(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-errCh:
				if !ok {
					return
				}
				return
			case ev, ok := <-eventCh:
				if !ok {
					return
				}
				if ev.Action == "die" {
					w.Tracker.SetExited(ev.InstanceID, 0) // exit code from Docker event isn't available, will be refined
				}
			}
		}
	}()
}

func (w *LocalWorker) GetAllInstanceStates(ctx context.Context) ([]worker.InstanceStateUpdate, error) {
	if w.Tracker == nil {
		return nil, nil
	}
	return w.Tracker.Snapshot(), nil
}

func (w *LocalWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return worker.PrepareGameScripts(w.GameStore, w.DataDir, gameID, gameserverID)
}

func (w *LocalWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	return worker.EnsureDepot(ctx, w.DataDir, w.Log, appID, branch, accountName, refreshToken, onProgress)
}

func (w *LocalWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	mountpoint, err := w.Resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return worker.CopyDepotToVolume(depotDir, mountpoint)
}

func (w *LocalWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	mountpoint, err := w.Resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return worker.DownloadWorkshopItem(ctx, w.DataDir, w.Log, appID, hcontentFile, filepath.Join(mountpoint, installPath))
}

func toDockerPorts(ports []worker.PortBinding) []PortBinding {
	out := make([]PortBinding, len(ports))
	for i, p := range ports {
		out[i] = PortBinding{
			HostPort:      p.HostPort,
			ContainerPort: p.InstancePort,
			Protocol:      p.Protocol,
		}
	}
	return out
}

// --- Copy operations (used by backup/restore) ---

func (w *LocalWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
	return w.Docker.CopyFromInstance(ctx, instanceID, path)
}

func (w *LocalWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
	return w.Docker.CopyToInstance(ctx, instanceID, path, content)
}

func (w *LocalWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	return w.Docker.CopyDirFromInstance(ctx, instanceID, path)
}

func (w *LocalWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	return w.Docker.CopyTarToInstance(ctx, instanceID, destPath, content)
}

func (w *LocalWorker) ListGameserverInstances(ctx context.Context) ([]worker.GameserverInstance, error) {
	containers, err := w.Docker.ListGameserverInstances(ctx)
	if err != nil {
		return nil, err
	}
	var result []worker.GameserverInstance
	for _, c := range containers {
		gsID, ok := naming.GameserverIDFromInstanceName(c.Name)
		if !ok {
			continue // update/fileops/backup instance, not a gameserver
		}
		result = append(result, worker.GameserverInstance{
			InstanceID:   c.ID,
			InstanceName: c.Name,
			GameserverID:  gsID,
			State:         c.State,
		})
	}
	return result, nil
}
