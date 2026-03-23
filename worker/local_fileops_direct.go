package worker

import (
	"context"
	"os"
	"sync"
)

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

// dockerVolumeResolver returns a volumeResolver that maps volume names to Docker mountpoints.
func (w *LocalWorker) dockerVolumeResolver() volumeResolver {
	var mu sync.RWMutex
	cache := make(map[string]string)

	return func(ctx context.Context, volumeName string) (string, error) {
		mu.RLock()
		if mp, ok := cache[volumeName]; ok {
			mu.RUnlock()
			return mp, nil
		}
		mu.RUnlock()

		mp, err := w.docker.VolumeMountpoint(ctx, volumeName)
		if err != nil {
			return "", err
		}

		mu.Lock()
		cache[volumeName] = mp
		mu.Unlock()
		return mp, nil
	}
}
