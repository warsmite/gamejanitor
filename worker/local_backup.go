package worker

import (
	"context"
	"fmt"
	"io"
)

// --- Volume-level backup operations ---

func (w *LocalWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if w.hasDirectAccess(ctx, volumeName) {
		return backupVolumeDirect(w.resolve, ctx, volumeName)
	}
	return w.backupVolumeSidecar(ctx, volumeName)
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
		return restoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
	}
	return w.restoreVolumeSidecar(ctx, volumeName, tarStream)
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

	return w.docker.CopyTarToContainer(ctx, sidecarID, "/", tarStream)
}
