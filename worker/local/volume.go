package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// --- Worker interface: Volumes ---

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	return nil
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	err := os.RemoveAll(path)
	if err != nil {
		// Files created inside a user namespace are owned by subordinate UIDs
		// that the current user can't delete. Run rm inside a throwaway
		// container with the same UID mapping — same approach as podman unshare.
		if rmErr := w.removeWithContainer(path); rmErr != nil {
			return fmt.Errorf("removing volume %s: %w (container fallback: %v)", name, err, rmErr)
		}
	}
	return nil
}

// removeWithContainer removes a path by running rm -rf inside a throwaway
// container that has the right UID mapping to access files created by game
// containers. Uses the host rootfs read-only (no image pull needed).
func (w *LocalWorker) removeWithContainer(path string) error {
	id := fmt.Sprintf("gj-cleanup-%d", os.Getpid())
	bundleDir := filepath.Join(w.dataDir, "instances", id)
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return fmt.Errorf("creating cleanup bundle dir: %w", err)
	}
	defer func() {
		runtime.CleanupBundle(bundleDir)
		os.RemoveAll(bundleDir)
	}()

	if err := runtime.PrepareBundle(bundleDir, runtime.BundleConfig{
		RootFS:  "/",
		Cmd:     []string{"/bin/rm", "-rf", "/cleanup-target"},
		WorkDir: "/",
		Binds: []runtime.Mount{
			{Source: path, Destination: "/cleanup-target", Options: []string{"rbind", "rw"}},
		},
	}); err != nil {
		return fmt.Errorf("preparing cleanup bundle: %w", err)
	}

	cmd := w.rt.RunSync(id, bundleDir)
	out, err := cmd.CombinedOutput()
	w.rt.Delete(id, true)
	if err != nil {
		return fmt.Errorf("cleanup container failed: %w\n%s", err, out)
	}

	// Container removed the contents; now remove the empty directory from the host
	return os.Remove(path)
}

func (w *LocalWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	return VolumeSizeDirect(w.resolve, ctx, volumeName)
}

// --- Worker interface: Backup/Restore ---

func (w *LocalWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return BackupVolumeDirect(w.resolve, ctx, volumeName)
}

func (w *LocalWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	return RestoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
}

// compile-time check: ensure LocalWorker satisfies the full Worker interface
var _ worker.Worker = (*LocalWorker)(nil)
