package local

import (
	"context"
	"sync"

	"github.com/warsmite/gamejanitor/worker"
)

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
