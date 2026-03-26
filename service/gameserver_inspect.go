package service

import (
	"context"
	"fmt"
	"io"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker"
)

func (s *GameserverService) GetContainerInfo(ctx context.Context, gameserverID string) (*worker.ContainerInfo, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).InspectContainer(ctx, *gs.ContainerID)
}

func (s *GameserverService) GetGameserverStats(ctx context.Context, gameserverID string) (*worker.GameserverStats, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	stats := &worker.GameserverStats{
		StorageLimitMB: gs.StorageLimitMB,
	}

	// Container stats only available when running
	if gs.ContainerID != nil {
		cs, err := w.ContainerStats(ctx, *gs.ContainerID)
		if err == nil {
			stats.MemoryUsageMB = cs.MemoryUsageMB
			stats.MemoryLimitMB = cs.MemoryLimitMB
			stats.CPUPercent = cs.CPUPercent
		} else {
			s.log.Debug("container stats unavailable", "gameserver_id", gameserverID, "error", err)
		}
	}

	// Volume size always available (only needs volume name)
	volSize, err := w.VolumeSize(ctx, gs.VolumeName)
	if err != nil {
		s.log.Debug("volume size unavailable", "gameserver_id", gameserverID, "error", err)
	} else {
		stats.VolumeSizeBytes = volSize
	}

	return stats, nil
}

func (s *GameserverService) GetVolumeSize(ctx context.Context, gameserverID string) (int64, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return 0, err
	}
	if gs == nil {
		return 0, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).VolumeSize(ctx, gs.VolumeName)
}

func (s *GameserverService) GetContainerLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).ContainerLogs(ctx, *gs.ContainerID, tail, false)
}
