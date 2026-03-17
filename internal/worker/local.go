package worker

import (
	"archive/tar"
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

	if err := w.docker.PullImage(ctx, fileopsImage); err != nil {
		return "", fmt.Errorf("pulling fileops image %s: %w", fileopsImage, err)
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
	containerPath := filepath.Join("/data", path)
	// Use stat with pipe-delimited format for reliable parsing (no locale/year issues)
	// sh -c is needed for the glob expansion
	cmd := []string{"sh", "-c", fmt.Sprintf(`stat -c '%%n|%%s|%%f|%%Y|%%F' %s/* %s/.* 2>/dev/null || true`, containerPath, containerPath)}
	exitCode, stdout, stderr, err := w.sidecarExec(ctx, volumeName, cmd)
	if err != nil {
		return nil, fmt.Errorf("listing directory %s: %w", path, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("listing directory %s: %s", path, stderr)
	}
	return parseStatOutput(stdout), nil
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

// parseStatOutput parses `stat -c '%n|%s|%f|%Y|%F'` output into FileEntry structs.
// Format: fullpath|size|hex_mode|unix_epoch|file_type
func parseStatOutput(output string) []FileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []FileEntry

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 5)
		if len(parts) < 5 {
			continue
		}

		name := filepath.Base(parts[0])
		if name == "." || name == ".." {
			continue
		}

		var fileSize int64
		fmt.Sscanf(parts[1], "%d", &fileSize)

		var modeHex uint32
		fmt.Sscanf(parts[2], "%x", &modeHex)
		perm := os.FileMode(modeHex)

		var epoch int64
		fmt.Sscanf(parts[3], "%d", &epoch)

		isDir := parts[4] == "directory"

		entries = append(entries, FileEntry{
			Name:        name,
			IsDir:       isDir,
			Size:        fileSize,
			Permissions: perm.String(),
			ModTime:     time.Unix(epoch, 0),
		})
	}

	sortFileEntries(entries)
	return entries
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

// --- Volume-level backup operations ---

func (w *LocalWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.backupVolumeDirect(ctx, volumeName)
	}
	return w.backupVolumeSidecar(ctx, volumeName)
}

func (w *LocalWorker) backupVolumeDirect(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	mountpoint, err := w.volumeMountpoint(ctx, volumeName)
	if err != nil {
		return nil, fmt.Errorf("resolving volume mountpoint: %w", err)
	}

	pr, pw := io.Pipe()
	tw := tar.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer tw.Close()

		err := filepath.Walk(mountpoint, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(mountpoint, path)
			if err != nil {
				return err
			}
			// tar paths should be under "data/" to match container layout
			tarPath := filepath.Join("data", relPath)

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = tarPath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			pw.CloseWithError(fmt.Errorf("creating tar from volume: %w", err))
		}
	}()

	return pr, nil
}

func (w *LocalWorker) backupVolumeSidecar(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	sidecarID, err := w.ensureSidecar(ctx, volumeName)
	if err != nil {
		return nil, fmt.Errorf("ensuring sidecar for backup: %w", err)
	}
	return w.docker.CopyDirFromContainer(ctx, sidecarID, "/data")
}

func (w *LocalWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	if w.hasDirectAccess(ctx, volumeName) {
		return w.restoreVolumeDirect(ctx, volumeName, tarStream)
	}
	return w.restoreVolumeSidecar(ctx, volumeName, tarStream)
}

func (w *LocalWorker) restoreVolumeDirect(ctx context.Context, volumeName string, tarStream io.Reader) error {
	mountpoint, err := w.volumeMountpoint(ctx, volumeName)
	if err != nil {
		return fmt.Errorf("resolving volume mountpoint: %w", err)
	}

	// Clear existing contents
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return fmt.Errorf("reading volume directory: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(mountpoint, entry.Name())); err != nil {
			return fmt.Errorf("clearing volume: %w", err)
		}
	}

	// Extract tar
	tr := tar.NewReader(tarStream)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Strip "data/" prefix to get path relative to volume root
		relPath := header.Name
		if strings.HasPrefix(relPath, "data/") {
			relPath = strings.TrimPrefix(relPath, "data/")
		} else if relPath == "data" {
			continue
		}
		if relPath == "" || relPath == "." {
			continue
		}

		targetPath := filepath.Join(mountpoint, filepath.Clean(relPath))
		if !strings.HasPrefix(targetPath, mountpoint) {
			continue // path traversal protection
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", relPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", relPath, err)
			}
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", relPath, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", relPath, err)
			}
			f.Close()
		}
	}

	return nil
}

func (w *LocalWorker) restoreVolumeSidecar(ctx context.Context, volumeName string, tarStream io.Reader) error {
	// Clear volume via remove + recreate
	if err := w.RemoveVolume(ctx, volumeName); err != nil {
		return fmt.Errorf("removing volume for restore: %w", err)
	}
	if err := w.CreateVolume(ctx, volumeName); err != nil {
		return fmt.Errorf("recreating volume for restore: %w", err)
	}

	// Get a fresh sidecar with the new volume
	sidecarID, err := w.ensureSidecar(ctx, volumeName)
	if err != nil {
		return fmt.Errorf("ensuring sidecar for restore: %w", err)
	}

	// Extract tar into sidecar's /data mount
	return w.docker.CopyTarToContainer(ctx, sidecarID, "/", tarStream)
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
