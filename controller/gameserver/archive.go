package gameserver

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/settings"
)

// Archive stops the gameserver, backs up its volume to archive storage,
// removes the instance and volume, and marks it as archived. Blocks until complete.
func (s *LifecycleService) Archive(ctx context.Context, id string) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	if gs.IsArchived() {
		return controller.ErrConflictf("gameserver %s is already archived", id)
	}
	if s.backupStore == nil {
		return controller.ErrBadRequest("backup storage is not configured, cannot archive")
	}

	// Stop if running
	if gs.Status != controller.StatusStopped {
		if s.statusProvider != nil {
			s.statusProvider.SetStopped(id)
		}
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, id, nil))

		if err := s.stopInstance(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver before archive: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		return fmt.Errorf("worker unavailable for gameserver %s", id)
	}

	// Backup volume to archive storage (gzipped tar)
	tarReader, err := w.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
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
		return fmt.Errorf("saving archive to store: %w", err)
	}
	if compressErr != nil {
		s.backupStore.DeleteArchive(ctx, id)
		return compressErr
	}

	s.log.Info("archive saved to store", "gameserver", id)

	// Remove instance and volume from worker
	if gs.InstanceID != nil {
		if err := w.RemoveInstance(ctx, *gs.InstanceID); err != nil {
			s.log.Warn("failed to remove instance during archive", "gameserver", id, "error", err)
		}
	}
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove volume during archive", "gameserver", id, "error", err)
	}

	// Update gameserver record
	gs.DesiredState = "archived"
	gs.InstanceID = nil
	gs.NodeID = nil
	if err := s.store.UpdateGameserver(gs); err != nil {
		return fmt.Errorf("updating gameserver as archived: %w", err)
	}

	s.log.Info("gameserver archived", "gameserver", id)
	return nil
}

// Unarchive restores an archived gameserver's volume from archive storage onto
// the target node (or auto-selected node). Blocks until complete.
func (s *LifecycleService) Unarchive(ctx context.Context, id string, targetNodeID string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	if !gs.IsArchived() {
		return controller.ErrConflictf("gameserver %s is not archived", id)
	}
	if s.backupStore == nil {
		return controller.ErrBadRequest("backup storage is not configured, cannot unarchive")
	}

	// Pick a node
	var nodeID string
	if targetNodeID != "" {
		nodeID = targetNodeID
	} else {
		candidates := s.dispatcher.RankWorkersForPlacement(gs.NodeTags)
		if len(candidates) == 0 {
			return controller.ErrUnavailable("no workers available for placement")
		}
		nodeID = candidates[0].NodeID
	}

	w, err := s.dispatcher.SelectWorkerByNodeID(nodeID)
	if err != nil || w == nil {
		return controller.ErrUnavailablef("worker %s unavailable", nodeID)
	}

	gs.DesiredState = "stopped"
	gs.NodeID = &nodeID
	if err := s.store.UpdateGameserver(gs); err != nil {
		return fmt.Errorf("updating gameserver desired state for unarchive: %w", err)
	}

	actor := event.ActorFromContext(ctx)

	// Create volume on target node
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("creating volume for unarchive: %w", err)
	}

	// Restore archive to volume
	reader, err := s.backupStore.LoadArchive(ctx, id)
	if err != nil {
		return fmt.Errorf("loading archive from store: %w", err)
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		return fmt.Errorf("decompressing archive: %w", err)
	}

	if err := w.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		return fmt.Errorf("restoring archive to volume: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Re-read to avoid stale data
	gs, err = s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found after restore", id)
	}

	// Reallocate ports if using per-node port scope
	if s.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		game := s.gameStore.GetGame(gs.GameID)
		if game == nil {
			return fmt.Errorf("game %s not found", gs.GameID)
		}
		newPorts, err := s.placement.ReallocatePorts(game, nodeID, "")
		if err != nil {
			return fmt.Errorf("allocating ports on target node: %w", err)
		}
		gs.Ports = newPorts
	}

	if err := s.store.UpdateGameserver(gs); err != nil {
		return fmt.Errorf("updating gameserver after unarchive: %w", err)
	}

	s.broadcaster.Publish(event.NewEvent(event.EventGameserverUnarchive, gs.ID, actor, &event.GameserverActionData{
		Gameserver: gs,
	}))

	s.log.Info("gameserver unarchived", "gameserver", id, "node", nodeID)
	return nil
}
