package local

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/warsmite/gamejanitor/worker"
)

// --- Worker interface: Volumes ---

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	// No chown. crun handles UID mapping via the OCI spec.
	return nil
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	err := os.RemoveAll(path)
	if err != nil {
		// Files created inside a user namespace are owned by subordinate UIDs
		// that the current user can't delete. Create a temporary user namespace
		// with the same UID mapping and delete from inside it.
		rmErr := removeWithUserNS(path, w.paths, w.log)
		if rmErr != nil {
			return fmt.Errorf("removing volume %s: %w (ns fallback: %v)", name, err, rmErr)
		}
	}
	return nil
}

// removeWithUserNS removes a path that may contain files owned by mapped UIDs.
func removeWithUserNS(path string, paths *systemPaths, log *slog.Logger) error {
	if paths.IsRoot {
		return exec.Command(paths.Rm, "-rf", path).Run()
	}

	holder := exec.Command(paths.Unshare, "--user", "--fork", "--kill-child", "--", paths.Sleep, "10")
	holder.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := holder.Start(); err != nil {
		return fmt.Errorf("starting cleanup namespace: %w", err)
	}
	defer func() { holder.Process.Kill(); holder.Wait() }()

	pid := holder.Process.Pid
	if paths.hasUIDMapping() {
		uid := os.Getuid()
		gid := os.Getgid()
		uidStart, uidCount := SubUIDRange()
		gidStart, gidCount := SubGIDRange()
		exec.Command(paths.NewUIDMap, fmt.Sprintf("%d", pid),
			"0", fmt.Sprintf("%d", uid), "1",
			"1", fmt.Sprintf("%d", uidStart), fmt.Sprintf("%d", uidCount)).Run()
		exec.Command(paths.NewGIDMap, fmt.Sprintf("%d", pid),
			"0", fmt.Sprintf("%d", gid), "1",
			"1", fmt.Sprintf("%d", gidStart), fmt.Sprintf("%d", gidCount)).Run()
	}

	return exec.Command(paths.Nsenter, fmt.Sprintf("--user=/proc/%d/ns/user", pid),
		"--", paths.Rm, "-rf", path).Run()
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
