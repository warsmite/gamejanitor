package gameserver

import (
	"context"
	"fmt"
	"io"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/worker"
)

func (s *LifecycleService) GetInstanceInfo(ctx context.Context, gameserverID string) (*worker.InstanceInfo, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.InstanceID == nil {
		return nil, fmt.Errorf("gameserver %s has no instance", gameserverID)
	}
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.InspectInstance(ctx, *gs.InstanceID)
}

func (s *LifecycleService) GetGameserverStats(ctx context.Context, gameserverID string) (*worker.GameserverStats, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	stats := &worker.GameserverStats{
		StorageLimitMB: gs.StorageLimitMB,
	}

	// Instance stats only available when running
	if gs.InstanceID != nil {
		cs, err := w.InstanceStats(ctx, *gs.InstanceID)
		if err == nil {
			stats.MemoryUsageMB = cs.MemoryUsageMB
			stats.MemoryLimitMB = cs.MemoryLimitMB
			stats.CPUPercent = cs.CPUPercent
		} else {
			s.log.Debug("instance stats unavailable", "gameserver", gameserverID, "error", err)
		}
	}

	// Volume size always available (only needs volume name)
	volSize, err := w.VolumeSize(ctx, gs.VolumeName)
	if err != nil {
		s.log.Debug("volume size unavailable", "gameserver", gameserverID, "error", err)
	} else {
		stats.VolumeSizeBytes = volSize
	}

	return stats, nil
}

func (s *LifecycleService) GetVolumeSize(ctx context.Context, gameserverID string) (int64, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return 0, err
	}
	if gs == nil {
		return 0, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return 0, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.VolumeSize(ctx, gs.VolumeName)
}

func (s *LifecycleService) GetInstanceLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.InstanceID == nil {
		return nil, fmt.Errorf("gameserver %s has no instance", gameserverID)
	}
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.InstanceLogs(ctx, *gs.InstanceID, tail, false)
}
