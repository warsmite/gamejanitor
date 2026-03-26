package local

import (
	"context"
	"os"
	"sync"

	"github.com/warsmite/gamejanitor/worker"
)

// --- Direct access detection ---

// HasDirectAccess probes once whether we can read Docker volume mountpoints.
func (w *LocalWorker) HasDirectAccess(ctx context.Context, volumeName string) bool {
	w.DirectAccessOnce.Do(func() {
		mp, err := w.Docker.VolumeMountpoint(ctx, volumeName)
		if err != nil {
			w.Log.Warn("cannot resolve volume mountpoint, using sidecar fallback for file operations", "error", err)
			return
		}
		_, err = os.Stat(mp)
		if err != nil {
			w.Log.Info("volume mountpoint not accessible, using sidecar fallback for file operations", "mountpoint", mp, "error", err)
			return
		}
		w.Log.Info("direct volume access available, using fast path for file operations", "mountpoint", mp)
		w.DirectAccess = true
	})
	return w.DirectAccess
}

// DockerVolumeResolver returns a VolumeResolver that maps volume names to Docker mountpoints.
func (w *LocalWorker) DockerVolumeResolver() worker.VolumeResolver {
	var mu sync.RWMutex
	cache := make(map[string]string)

	return func(ctx context.Context, volumeName string) (string, error) {
		mu.RLock()
		if mp, ok := cache[volumeName]; ok {
			mu.RUnlock()
			return mp, nil
		}
		mu.RUnlock()

		mp, err := w.Docker.VolumeMountpoint(ctx, volumeName)
		if err != nil {
			return "", err
		}

		mu.Lock()
		cache[volumeName] = mp
		mu.Unlock()
		return mp, nil
	}
}
