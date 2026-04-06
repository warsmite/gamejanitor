package lifecycle

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
	"github.com/warsmite/gamejanitor/controller/settings"
)

// MigrateGameserver validates the migration request and transfers the gameserver
// to the target node. Stops the gameserver if running, transfers volume data via
// the backup store, and optionally restarts. Blocks until complete.
func (s *Service) MigrateGameserver(ctx context.Context, gameserverID string, targetNodeID string, onProgress operation.ProgressFunc) error {
	gs, err := s.getGameserverWithStatus(gameserverID)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	currentNodeID := ""
	if gs.NodeID != nil {
		currentNodeID = *gs.NodeID
	}
	if currentNodeID == targetNodeID {
		return controller.ErrBadRequestf("gameserver is already on node %s", targetNodeID)
	}

	_, err = s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return controller.ErrUnavailablef("target worker unavailable: %v", err)
	}

	if !gs.NodeTags.IsEmpty() {
		targetNode, err := s.store.GetWorkerNode(targetNodeID)
		if err != nil || targetNode == nil {
			return controller.ErrNotFoundf("target node %s not found", targetNodeID)
		}
		if !targetNode.Tags.HasAll(gs.NodeTags) {
			return controller.ErrBadRequestf("target node %s missing required labels: %v", targetNodeID, gs.NodeTags)
		}
	}

	if err := s.placement.CheckWorkerLimits(targetNodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
		return err
	}

	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		return controller.ErrUnavailable("source worker is offline, cannot migrate (both workers must be online)")
	}

	return s.doMigrate(ctx, gameserverID, targetNodeID)
}

// doMigrate performs the migration work. Separated so startInstance can call it
// for auto-migration before start without re-validating.
func (s *Service) doMigrate(ctx context.Context, gameserverID string, targetNodeID string) error {
	gs, err := s.getGameserverWithStatus(gameserverID)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", gameserverID)
	}

	currentNodeID := ""
	if gs.NodeID != nil {
		currentNodeID = *gs.NodeID
	}

	s.log.Info("migrating gameserver", "gameserver", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)

	wasRunning := gs.Status != controller.StatusStopped
	if wasRunning {
		s.log.Info("stopping gameserver for migration", "gameserver", gameserverID)

		if s.statusProvider != nil {
			s.statusProvider.SetStopped(gameserverID)
		}
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, gameserverID, nil))

		if err := s.stopInstance(ctx, gameserverID); err != nil {
			s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
			return fmt.Errorf("stopping gameserver for migration: %w", err)
		}
	}

	// Re-read after stop — stopInstance updates InstanceID and DesiredState in DB
	gs, err = s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		return fmt.Errorf("re-reading gameserver %s after stop for migration: %w", gameserverID, err)
	}

	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", fmt.Errorf("source worker offline")))
		return fmt.Errorf("source worker went offline during migration")
	}

	targetWorker, err := s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil || targetWorker == nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", fmt.Errorf("target worker offline")))
		return fmt.Errorf("target worker went offline during migration")
	}

	// Transfer volume via backup store
	s.log.Info("transferring volume data via store", "gameserver", gameserverID, "volume", gs.VolumeName)
	migrationID := uuid.New().String()

	tarReader, err := sourceWorker.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("reading volume from source worker: %w", err)
	}
	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = err
			gzWriter.Close()
			pw.CloseWithError(err)
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

	if err := s.backupStore.Save(ctx, "migrations", migrationID, pr); err != nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("saving migration data to store: %w", err)
	}
	if compressErr != nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", compressErr))
		return fmt.Errorf("compressing volume data: %w", compressErr)
	}
	s.log.Info("migration data stored", "gameserver", gameserverID, "migration", migrationID)

	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("creating volume on target worker: %w", err)
	}

	reader, err := s.backupStore.Load(ctx, "migrations", migrationID)
	if err != nil {
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("loading migration data from store: %w", err)
	}
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("decompressing migration data: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up target volume after failed restore", "volume", gs.VolumeName, "error", rmErr)
		}
		s.log.Error("migration restore failed, data preserved in store", "migration", migrationID)
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("restoring volume on target worker: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Verify the target volume actually has data before deleting anything
	targetSize, err := targetWorker.VolumeSize(ctx, gs.VolumeName)
	if err != nil {
		s.log.Error("migration: failed to verify target volume, aborting — source volume preserved", "migration", migrationID, "error", err)
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", err))
		return fmt.Errorf("verifying target volume after restore: %w", err)
	}
	if targetSize == 0 {
		s.log.Error("migration: target volume is empty after restore, aborting — source volume preserved", "migration", migrationID, "volume", gs.VolumeName)
		emptyErr := fmt.Errorf("target volume is empty after restore — data may not have transferred. Source volume preserved on %s, migration data preserved in store as %s", currentNodeID, migrationID)
		s.setError(gameserverID, controller.OperationFailedReason("Migration failed", emptyErr))
		return emptyErr
	}
	s.log.Info("migration restore verified", "gameserver", gameserverID, "target_volume_bytes", targetSize)

	gs.NodeID = &targetNodeID

	if s.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		game := s.gameStore.GetGame(gs.GameID)
		if game == nil {
			return fmt.Errorf("game %s not found", gs.GameID)
		}
		newPorts, err := s.placement.ReallocatePorts(game, targetNodeID, "")
		if err != nil {
			return fmt.Errorf("allocating ports on target node: %w", err)
		}
		gs.Ports = newPorts
	}

	if err := s.store.UpdateGameserver(gs); err != nil {
		return fmt.Errorf("updating gameserver node assignment: %w", err)
	}

	if err := sourceWorker.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove old volume from source worker", "volume", gs.VolumeName, "error", err)
	}

	if err := s.backupStore.Delete(ctx, "migrations", migrationID); err != nil {
		s.log.Warn("failed to clean up migration data from store", "migration", migrationID, "error", err)
	}

	s.log.Info("gameserver migrated", "gameserver", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)

	if wasRunning {
		s.log.Info("restarting gameserver after migration", "gameserver", gameserverID)
		if err := s.startInstance(ctx, gameserverID, nil); err != nil {
			s.log.Error("failed to restart gameserver after migration", "gameserver", gameserverID, "error", err)
			s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopped, gameserverID, nil))
		}
	} else {
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopped, gameserverID, nil))
	}

	return nil
}
