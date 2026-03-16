package worker

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
)

const fileopsImage = "alpine:latest"

// LocalWorker implements Worker by delegating to the Docker client.
// Used in standalone mode where controller and worker run in the same process.
type LocalWorker struct {
	docker    *docker.Client
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string

	mountMu    sync.RWMutex
	mountCache map[string]string // volume name → mountpoint

	// Direct volume access detection (probed once on first file op)
	directAccessOnce sync.Once
	directAccess     bool

	// Lazy sidecar containers for when direct volume access is unavailable
	sidecarMu    sync.Mutex
	sidecarCache map[string]string // volume name → container ID
}

func NewLocalWorker(dockerClient *docker.Client, gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	return &LocalWorker{
		docker:       dockerClient,
		log:          log,
		gameStore:    gameStore,
		dataDir:      dataDir,
		mountCache:   make(map[string]string),
		sidecarCache: make(map[string]string),
	}
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
	w.mountMu.Lock()
	delete(w.mountCache, name)
	w.mountMu.Unlock()

	w.removeSidecar(context.Background(), name)

	return w.docker.RemoveVolume(ctx, name)
}

// --- Volume file operations ---
// Uses direct filesystem access when the host volume mountpoints are accessible,
// falls back to a lazy sidecar container when running inside Docker.

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.listFilesDirect(ctx, volumeName, path)
	}
	return w.listFilesSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.readFileDirect(ctx, volumeName, path)
	}
	return w.readFileSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.writeFileDirect(ctx, volumeName, path, content, perm)
	}
	return w.writeFileSidecar(ctx, volumeName, path, content)
}

func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.deletePathDirect(ctx, volumeName, path)
	}
	return w.deletePathSidecar(ctx, volumeName, path)
}

func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.createDirectoryDirect(ctx, volumeName, path)
	}
	return w.createDirectorySidecar(ctx, volumeName, path)
}

func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.renamePathDirect(ctx, volumeName, from, to)
	}
	return w.renamePathSidecar(ctx, volumeName, from, to)
}

// --- Direct access detection ---

// hasDirectAccess probes once whether we can read Docker volume mountpoints.
func (w *LocalWorker) hasDirectAccess(ctx context.Context, volumeName string) bool {
	w.directAccessOnce.Do(func() {
		mp, err := w.docker.VolumeMountpoint(ctx, volumeName)
		if err != nil {
			w.log.Warn("cannot resolve volume mountpoint, using sidecar fallback for file operations", "error", err)
			return
		}
		_, err = os.Stat(mp)
		if err != nil {
			w.log.Info("volume mountpoint not accessible, using sidecar fallback for file operations", "mountpoint", mp, "error", err)
			return
		}
		w.log.Info("direct volume access available, using fast path for file operations", "mountpoint", mp)
		w.directAccess = true
	})
	return w.directAccess
}

// --- Direct filesystem implementation ---

func (w *LocalWorker) volumePath(ctx context.Context, volumeName string, relPath string) (string, error) {
	mountpoint, err := w.volumeMountpoint(ctx, volumeName)
	if err != nil {
		return "", err
	}

	resolved := filepath.Join(mountpoint, filepath.Clean(relPath))
	if !strings.HasPrefix(resolved, mountpoint) {
		return "", fmt.Errorf("path %q escapes volume root", relPath)
	}
	return resolved, nil
}

func (w *LocalWorker) volumeMountpoint(ctx context.Context, volumeName string) (string, error) {
	w.mountMu.RLock()
	if mp, ok := w.mountCache[volumeName]; ok {
		w.mountMu.RUnlock()
		return mp, nil
	}
	w.mountMu.RUnlock()

	mp, err := w.docker.VolumeMountpoint(ctx, volumeName)
	if err != nil {
		return "", err
	}

	w.mountMu.Lock()
	w.mountCache[volumeName] = mp
	w.mountMu.Unlock()
	return mp, nil
}

func (w *LocalWorker) listFilesDirect(ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	hostPath, err := w.volumePath(ctx, volumeName, path)
	if err != nil {
		return nil, err
	}

	dirEntries, err := os.ReadDir(hostPath)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", path, err)
	}

	entries := make([]FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, FileEntry{
			Name:        de.Name(),
			IsDir:       de.IsDir(),
			Size:        info.Size(),
			ModTime:     info.ModTime(),
			Permissions: info.Mode().String(),
		})
	}

	sortFileEntries(entries)
	return entries, nil
}

func (w *LocalWorker) readFileDirect(ctx context.Context, volumeName string, path string) ([]byte, error) {
	hostPath, err := w.volumePath(ctx, volumeName, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(hostPath)
}

func (w *LocalWorker) writeFileDirect(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	hostPath, err := w.volumePath(ctx, volumeName, path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(hostPath, content, perm); err != nil {
		return err
	}
	return os.Chown(hostPath, 1001, 1001)
}

func (w *LocalWorker) deletePathDirect(ctx context.Context, volumeName string, path string) error {
	hostPath, err := w.volumePath(ctx, volumeName, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(hostPath)
}

func (w *LocalWorker) createDirectoryDirect(ctx context.Context, volumeName string, path string) error {
	hostPath, err := w.volumePath(ctx, volumeName, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hostPath, fs.ModePerm); err != nil {
		return err
	}
	return filepath.WalkDir(hostPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, 1001, 1001)
	})
}

func (w *LocalWorker) renamePathDirect(ctx context.Context, volumeName string, from string, to string) error {
	fromPath, err := w.volumePath(ctx, volumeName, from)
	if err != nil {
		return err
	}
	toPath, err := w.volumePath(ctx, volumeName, to)
	if err != nil {
		return err
	}
	return os.Rename(fromPath, toPath)
}

// --- Sidecar fallback implementation ---
// Used when direct volume access is unavailable (e.g., running inside Docker).
// Creates a lazy Alpine container per volume on first access.

func (w *LocalWorker) ensureSidecar(ctx context.Context, volumeName string) (string, error) {
	w.sidecarMu.Lock()
	defer w.sidecarMu.Unlock()

	if id, ok := w.sidecarCache[volumeName]; ok {
		// Verify it's still running
		info, err := w.docker.InspectContainer(ctx, id)
		if err == nil && info.State == "running" {
			return id, nil
		}
		// Gone or stopped — remove from cache and recreate
		w.docker.RemoveContainer(ctx, id)
		delete(w.sidecarCache, volumeName)
	}

	// Also try by name in case a previous run left one behind
	containerName := "gamejanitor-fileops-" + volumeName
	info, err := w.docker.InspectContainer(ctx, containerName)
	if err == nil {
		if info.State == "running" {
			w.sidecarCache[volumeName] = info.ID
			return info.ID, nil
		}
		if startErr := w.docker.StartContainer(ctx, info.ID); startErr == nil {
			w.sidecarCache[volumeName] = info.ID
			return info.ID, nil
		}
		w.docker.RemoveContainer(ctx, info.ID)
	}

	containerID, err := w.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       containerName,
		Image:      fileopsImage,
		Env:        []string{},
		VolumeName: volumeName,
		Entrypoint: []string{"sleep", "infinity"},
		User:       "1001:1001",
	})
	if err != nil {
		return "", fmt.Errorf("creating fileops sidecar for volume %s: %w", volumeName, err)
	}

	if err := w.docker.StartContainer(ctx, containerID); err != nil {
		w.docker.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("starting fileops sidecar for volume %s: %w", volumeName, err)
	}

	w.log.Info("created fileops sidecar", "volume", volumeName, "container_id", containerID[:12])
	w.sidecarCache[volumeName] = containerID
	return containerID, nil
}

func (w *LocalWorker) removeSidecar(ctx context.Context, volumeName string) {
	w.sidecarMu.Lock()
	id, ok := w.sidecarCache[volumeName]
	delete(w.sidecarCache, volumeName)
	w.sidecarMu.Unlock()

	if ok {
		if err := w.docker.RemoveContainer(ctx, id); err != nil {
			w.log.Debug("failed to remove sidecar by id", "volume", volumeName, "error", err)
		}
	}
	// Also try by name
	containerName := "gamejanitor-fileops-" + volumeName
	if err := w.docker.RemoveContainer(ctx, containerName); err != nil {
		w.log.Debug("no sidecar to remove by name", "volume", volumeName)
	}
}

func (w *LocalWorker) sidecarExec(ctx context.Context, volumeName string, cmd []string) (int, string, string, error) {
	containerID, err := w.ensureSidecar(ctx, volumeName)
	if err != nil {
		return -1, "", "", err
	}
	return w.docker.Exec(ctx, containerID, cmd)
}

func (w *LocalWorker) listFilesSidecar(ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	// /data is where the volume is mounted inside the sidecar
	containerPath := filepath.Join("/data", path)
	exitCode, stdout, stderr, err := w.sidecarExec(ctx, volumeName, []string{"ls", "-la", containerPath})
	if err != nil {
		return nil, fmt.Errorf("listing directory %s: %w", path, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("listing directory %s: %s", path, stderr)
	}
	return parseLsOutput(stdout), nil
}

func (w *LocalWorker) readFileSidecar(ctx context.Context, volumeName string, path string) ([]byte, error) {
	containerID, err := w.ensureSidecar(ctx, volumeName)
	if err != nil {
		return nil, err
	}
	containerPath := filepath.Join("/data", path)
	return w.docker.CopyFromContainer(ctx, containerID, containerPath)
}

func (w *LocalWorker) writeFileSidecar(ctx context.Context, volumeName string, path string, content []byte) error {
	containerID, err := w.ensureSidecar(ctx, volumeName)
	if err != nil {
		return err
	}
	containerPath := filepath.Join("/data", path)
	return w.docker.CopyToContainer(ctx, containerID, containerPath, content)
}

func (w *LocalWorker) deletePathSidecar(ctx context.Context, volumeName string, path string) error {
	containerPath := filepath.Join("/data", path)
	exitCode, _, stderr, err := w.sidecarExec(ctx, volumeName, []string{"rm", "-rf", containerPath})
	if err != nil {
		return fmt.Errorf("deleting %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("deleting %s: %s", path, stderr)
	}
	return nil
}

func (w *LocalWorker) createDirectorySidecar(ctx context.Context, volumeName string, path string) error {
	containerPath := filepath.Join("/data", path)
	exitCode, _, stderr, err := w.sidecarExec(ctx, volumeName, []string{"mkdir", "-p", containerPath})
	if err != nil {
		return fmt.Errorf("creating directory %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("creating directory %s: %s", path, stderr)
	}
	return nil
}

func (w *LocalWorker) renamePathSidecar(ctx context.Context, volumeName string, from string, to string) error {
	fromPath := filepath.Join("/data", from)
	toPath := filepath.Join("/data", to)
	exitCode, _, stderr, err := w.sidecarExec(ctx, volumeName, []string{"mv", fromPath, toPath})
	if err != nil {
		return fmt.Errorf("renaming %s to %s: %w", from, to, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("renaming %s to %s: %s", from, to, stderr)
	}
	return nil
}

// parseLsOutput parses `ls -la` output into FileEntry structs.
// Used by the sidecar fallback path.
func parseLsOutput(output string) []FileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []FileEntry

	for _, line := range lines {
		if strings.HasPrefix(line, "total ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		name := strings.Join(fields[8:], " ")
		if idx := strings.Index(name, " -> "); idx >= 0 {
			name = name[:idx]
		}
		if name == "." || name == ".." {
			continue
		}

		perms := fields[0]
		isDir := len(perms) > 0 && perms[0] == 'd'
		size, _ := fmt.Sscanf(fields[4], "%d", new(int64))
		_ = size

		var fileSize int64
		fmt.Sscanf(fields[4], "%d", &fileSize)

		// ls -la time format varies; store as-is — the template handles display
		modTimeStr := fields[5] + " " + fields[6] + " " + fields[7]

		entries = append(entries, FileEntry{
			Name:        name,
			IsDir:       isDir,
			Size:        fileSize,
			Permissions: perms,
			// ModTime from ls is a string, but FileEntry.ModTime is time.Time.
			// Parse best-effort; zero time on failure is acceptable for the fallback.
			ModTime: parseLsTime(modTimeStr),
		})
	}

	sortFileEntries(entries)
	return entries
}

// parseLsTime parses the time format from `ls -la` output.
// Returns zero time on failure — acceptable for the fallback path.
func parseLsTime(s string) time.Time {
	// ls outputs either "Jan  2 15:04" (recent) or "Jan  2  2006" (old)
	for _, layout := range []string{
		"Jan _2 15:04",
		"Jan _2  2006",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// --- Shared helpers ---

func sortFileEntries(entries []FileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
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
	gsDir := filepath.Join(w.dataDir, "gameservers", gameserverID)
	if err := w.gameStore.ExtractScripts(gameID, gsDir); err != nil {
		return "", "", fmt.Errorf("extracting scripts for %s: %w", gameserverID, err)
	}

	scriptDir := filepath.Join(gsDir, "scripts")
	defaultsDir := filepath.Join(gsDir, "defaults")

	// Only return defaults dir if it exists
	if _, err := os.Stat(defaultsDir); err != nil {
		defaultsDir = ""
	}

	return scriptDir, defaultsDir, nil
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
