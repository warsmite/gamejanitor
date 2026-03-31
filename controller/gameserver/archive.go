package gameserver

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
)

// Archive stops the gameserver, backs up its volume to archive storage,
// removes the container and volume from the worker, and marks it archived.
func (s *GameserverService) Archive(ctx context.Context, id string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	if gs.Archived {
		return controller.ErrConflictf("gameserver %s is already archived", id)
	}
	if s.backupStore == nil {
		return controller.ErrBadRequest("backup storage is not configured, cannot archive")
	}

	// Stop if running
	if gs.Status != controller.StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver before archive: %w", err)
		}
	}

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, _ := s.trackActivity(ctx, id, workerID, model.OpArchive, nil, nil)
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		if opID != "" {
			s.failActivity(id, fmt.Errorf("worker unavailable"))
		}
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	// Backup volume to archive storage (gzipped tar)
	tarReader, err := w.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("backing up volume for archive: %w", err)
	}

	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = fmt.Errorf("compressing archive: %w", err)
			gzWriter.Close()
			pw.CloseWithError(compressErr)
			tarReader.Close()
			return
		}
		tarReader.Close()
		if err := gzWriter.Close(); err != nil {
			compressErr = fmt.Errorf("closing gzip writer: %w", err)
			pw.CloseWithError(compressErr)
			return
		}
		pw.Close()
	}()

	if err := s.backupStore.SaveArchive(ctx, id, pr); err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("saving archive to store: %w", err)
	}
	if compressErr != nil {
		s.backupStore.DeleteArchive(ctx, id)
		if opID != "" {
			s.failActivity(id, compressErr)
		}
		return compressErr
	}

	s.log.Info("archive saved to store", "gameserver", id)

	// Remove container and volume from worker
	if gs.InstanceID != nil {
		if err := w.RemoveInstance(ctx, *gs.InstanceID); err != nil {
			s.log.Warn("failed to remove container during archive", "gameserver", id, "error", err)
		}
	}
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove volume during archive", "gameserver", id, "error", err)
	}

	// Update gameserver record
	gs.Archived = true
	gs.Status = controller.StatusArchived
	gs.InstanceID = nil
	gs.NodeID = nil
	if err := s.store.UpdateGameserver(gs); err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("updating gameserver as archived: %w", err)
	}

	if opID != "" {
		s.completeActivity(id)
	}

	s.log.Info("gameserver archived", "gameserver", id)
	return nil
}

// Unarchive restores an archived gameserver. If targetNodeID is empty, a node
// is selected automatically via placement ranking.
func (s *GameserverService) Unarchive(ctx context.Context, id string, targetNodeID string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	if !gs.Archived {
		return controller.ErrConflictf("gameserver %s is not archived", id)
	}
	if s.backupStore == nil {
		return controller.ErrBadRequest("backup storage is not configured, cannot unarchive")
	}

	actor := controller.ActorFromContext(ctx)

	// Pick a node — use explicit target or auto-place
	var nodeID string
	if targetNodeID != "" {
		nodeID = targetNodeID
	} else {
		s.placementMu.Lock()
		candidates := s.dispatcher.RankWorkersForPlacement(gs.NodeTags)
		if len(candidates) == 0 {
			s.placementMu.Unlock()
			return controller.ErrUnavailable("no workers available for placement")
		}
		nodeID = candidates[0].NodeID
		s.placementMu.Unlock()
	}

	opID, _ := s.trackActivity(ctx, id, nodeID, model.OpUnarchive, nil, nil)
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
	}

	w, err := s.dispatcher.SelectWorkerByNodeID(nodeID)
	if err != nil || w == nil {
		if opID != "" {
			s.failActivity(id, fmt.Errorf("worker unavailable"))
		}
		return controller.ErrUnavailablef("worker %s unavailable", nodeID)
	}

	// Create volume on target node
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("creating volume for unarchive: %w", err)
	}

	// Restore archive to volume
	reader, err := s.backupStore.LoadArchive(ctx, id)
	if err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("loading archive from store: %w", err)
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("decompressing archive: %w", err)
	}

	if err := w.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("restoring archive to volume: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Update gameserver record
	gs.Archived = false
	gs.Status = controller.StatusStopped
	gs.NodeID = &nodeID

	// Reallocate ports if using per-node port scope
	if s.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		s.placementMu.Lock()
		game := s.gameStore.GetGame(gs.GameID)
		if game == nil {
			s.placementMu.Unlock()
			if opID != "" {
				s.failActivity(id, fmt.Errorf("game %s not found", gs.GameID))
			}
			return controller.ErrNotFoundf("game %s not found", gs.GameID)
		}
		newPorts, err := s.AllocatePorts(game, nodeID, "")
		if err != nil {
			s.placementMu.Unlock()
			if opID != "" {
				s.failActivity(id, err)
			}
			return fmt.Errorf("allocating ports on target node: %w", err)
		}
		gs.Ports = newPorts
		s.placementMu.Unlock()
	}

	if err := s.store.UpdateGameserver(gs); err != nil {
		if opID != "" {
			s.failActivity(id, err)
		}
		return fmt.Errorf("updating gameserver after unarchive: %w", err)
	}

	if opID != "" {
		s.completeActivity(id)
	}

	actorJSON, _ := json.Marshal(actor)
	dataJSON, _ := json.Marshal(gs)
	s.recordInstant(&gs.ID, controller.EventGameserverUnarchive, actorJSON, dataJSON)

	s.log.Info("gameserver unarchived", "gameserver", id, "node", nodeID)
	return nil
}
