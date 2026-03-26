package local

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/docker"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

const fileopsImage = "alpine:latest"

// --- Sidecar fallback implementation ---
// Used when direct volume access is unavailable (e.g., running inside Docker).
// Creates a lazy Alpine container per volume on first access.

func (w *LocalWorker) EnsureSidecar(ctx context.Context, volumeName string) (string, error) {
	w.SidecarMu.Lock()
	defer w.SidecarMu.Unlock()

	if id, ok := w.SidecarCache[volumeName]; ok {
		// Verify it's still running
		info, err := w.Docker.InspectContainer(ctx, id)
		if err == nil && info.State == "running" {
			return id, nil
		}
		// Gone or stopped — remove from cache and recreate
		w.Docker.RemoveContainer(ctx, id)
		delete(w.SidecarCache, volumeName)
	}

	// Also try by name in case a previous run left one behind
	containerName := naming.FileopsContainerName(volumeName)
	info, err := w.Docker.InspectContainer(ctx, containerName)
	if err == nil {
		if info.State == "running" {
			w.SidecarCache[volumeName] = info.ID
			return info.ID, nil
		}
		if startErr := w.Docker.StartContainer(ctx, info.ID); startErr == nil {
			w.SidecarCache[volumeName] = info.ID
			return info.ID, nil
		}
		w.Docker.RemoveContainer(ctx, info.ID)
	}

	if err := w.Docker.PullImage(ctx, fileopsImage); err != nil {
		return "", fmt.Errorf("pulling fileops image %s: %w", fileopsImage, err)
	}

	containerID, err := w.Docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       containerName,
		Image:      fileopsImage,
		Env:        []string{},
		VolumeName: volumeName,
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return "", fmt.Errorf("creating fileops sidecar for volume %s: %w", volumeName, err)
	}

	if err := w.Docker.StartContainer(ctx, containerID); err != nil {
		w.Docker.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("starting fileops sidecar for volume %s: %w", volumeName, err)
	}

	w.Log.Info("created fileops sidecar", "volume", volumeName, "container_id", containerID[:12])
	w.SidecarCache[volumeName] = containerID
	return containerID, nil
}

func (w *LocalWorker) RemoveSidecar(ctx context.Context, volumeName string) {
	w.SidecarMu.Lock()
	id, ok := w.SidecarCache[volumeName]
	delete(w.SidecarCache, volumeName)
	w.SidecarMu.Unlock()

	if ok {
		if err := w.Docker.RemoveContainer(ctx, id); err != nil {
			w.Log.Debug("failed to remove sidecar by id", "volume", volumeName, "error", err)
		}
	}
	// Also try by name
	containerName := naming.FileopsContainerName(volumeName)
	if err := w.Docker.RemoveContainer(ctx, containerName); err != nil {
		w.Log.Debug("no sidecar to remove by name", "volume", volumeName)
	}
}

func (w *LocalWorker) SidecarExec(ctx context.Context, volumeName string, cmd []string) (int, string, string, error) {
	containerID, err := w.EnsureSidecar(ctx, volumeName)
	if err != nil {
		return -1, "", "", err
	}
	return w.Docker.Exec(ctx, containerID, cmd)
}

// sidecarPath resolves a relative path to an absolute container path within /data.
// Returns an error if the resolved path escapes /data (traversal attack).
func sidecarPath(relPath string) (string, error) {
	resolved := filepath.Join("/data", filepath.Clean(relPath))
	if !strings.HasPrefix(resolved, "/data") {
		return "", fmt.Errorf("path %q escapes /data", relPath)
	}
	return resolved, nil
}

func (w *LocalWorker) listFilesSidecar(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	containerPath, err := sidecarPath(path)
	if err != nil {
		return nil, err
	}
	// Use stat with pipe-delimited format for reliable parsing (no locale/year issues)
	// sh -c is needed for the glob expansion
	cmd := []string{"sh", "-c", fmt.Sprintf(`stat -c '%%n|%%s|%%f|%%Y|%%F' %s/* %s/.* 2>/dev/null || true`, containerPath, containerPath)}
	exitCode, stdout, stderr, err := w.SidecarExec(ctx, volumeName, cmd)
	if err != nil {
		return nil, fmt.Errorf("listing directory %s: %w", path, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("listing directory %s: %s", path, stderr)
	}
	return parseStatOutput(stdout), nil
}

func (w *LocalWorker) readFileSidecar(ctx context.Context, volumeName string, path string) ([]byte, error) {
	containerID, err := w.EnsureSidecar(ctx, volumeName)
	if err != nil {
		return nil, err
	}
	containerPath, err := sidecarPath(path)
	if err != nil {
		return nil, err
	}
	return w.Docker.CopyFromContainer(ctx, containerID, containerPath)
}

func (w *LocalWorker) writeFileSidecar(ctx context.Context, volumeName string, path string, content []byte) error {
	containerID, err := w.EnsureSidecar(ctx, volumeName)
	if err != nil {
		return err
	}
	containerPath, err := sidecarPath(path)
	if err != nil {
		return err
	}
	if err := w.Docker.CopyToContainer(ctx, containerID, containerPath, content); err != nil {
		return err
	}
	// Sidecar runs as root — chown so game server can access the file
	w.SidecarExec(ctx, volumeName, []string{"chown", fmt.Sprintf("%d:%d", model.GameserverUID, model.GameserverGID), containerPath})
	return nil
}

func (w *LocalWorker) deletePathSidecar(ctx context.Context, volumeName string, path string) error {
	containerPath, err := sidecarPath(path)
	if err != nil {
		return err
	}
	exitCode, _, stderr, err := w.SidecarExec(ctx, volumeName, []string{"rm", "-rf", containerPath})
	if err != nil {
		return fmt.Errorf("deleting %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("deleting %s: %s", path, stderr)
	}
	return nil
}

func (w *LocalWorker) createDirectorySidecar(ctx context.Context, volumeName string, path string) error {
	containerPath, err := sidecarPath(path)
	if err != nil {
		return err
	}
	exitCode, _, stderr, err := w.SidecarExec(ctx, volumeName, []string{"mkdir", "-p", containerPath})
	if err != nil {
		return fmt.Errorf("creating directory %s: %w", path, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("creating directory %s: %s", path, stderr)
	}
	// Sidecar runs as root — chown so game server (1001:1001) can access the directory
	w.SidecarExec(ctx, volumeName, []string{"chown", "1001:1001", containerPath})
	return nil
}

func (w *LocalWorker) renamePathSidecar(ctx context.Context, volumeName string, from string, to string) error {
	fromPath := filepath.Join("/data", from)
	toPath := filepath.Join("/data", to)
	exitCode, _, stderr, err := w.SidecarExec(ctx, volumeName, []string{"mv", fromPath, toPath})
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
func parseStatOutput(output string) []worker.FileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []worker.FileEntry

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

		entries = append(entries, worker.FileEntry{
			Name:        name,
			IsDir:       isDir,
			Size:        fileSize,
			Permissions: perm.String(),
			ModTime:     time.Unix(epoch, 0),
		})
	}

	worker.SortFileEntries(entries)
	return entries
}
