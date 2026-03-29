package local

import (
	"context"
	"io"

	"github.com/warsmite/gamejanitor/worker"
)

func (w *LocalWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return worker.BackupVolumeDirect(w.Resolve, ctx, volumeName)
}

func (w *LocalWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	return worker.RestoreVolumeDirect(w.Resolve, ctx, volumeName, tarStream)
}
