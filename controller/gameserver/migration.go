package gameserver

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
)

// MigrateGameserver validates the migration request and launches the heavy work
// (stop, transfer, restore, restart) in a background goroutine via runOperation.
// Requires both source and target workers to be online.
func (s *GameserverService) MigrateGameserver(ctx context.Context, gameserverID string, targetNodeID string) error {
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

	// Validate target worker is connected
	_, err = s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return controller.ErrUnavailablef("target worker unavailable: %v", err)
	}

	// Validate target node labels
	if !gs.NodeTags.IsEmpty() {
		targetNode, err := s.store.GetWorkerNode(targetNodeID)
		if err != nil || targetNode == nil {
			return controller.ErrNotFoundf("target node %s not found", targetNodeID)
		}
		if !targetNode.Tags.HasAll(gs.NodeTags) {
			return controller.ErrBadRequestf("target node %s missing required labels: %v", targetNodeID, gs.NodeTags)
		}
	}

	// Check target node limits
	if err := s.placement.CheckWorkerLimits(targetNodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
		return err
	}

	// Verify source worker is online (must be online to read volume data)
	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		return controller.ErrUnavailable("source worker is offline, cannot migrate (both workers must be online)")
	}

	return s.runOperation(ctx, gameserverID, currentNodeID, model.OpMigrate, func(ctx context.Context) error {
		return s.doMigrate(ctx, gameserverID, targetNodeID)
	})
}

// doMigrate performs the heavy work of migrating a gameserver between nodes.
// Runs in a background goroutine — re-reads the gameserver from DB since state
// may have changed.
func (s *GameserverService) doMigrate(ctx context.Context, gameserverID string, targetNodeID string) error {
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

	// Stop if running — remember prior state so we can restart after migration.
	// Use doStop directly since we're already inside runOperation.
	wasRunning := gs.Status != controller.StatusStopped
	if wasRunning {
		s.log.Info("stopping gameserver for migration", "gameserver", gameserverID)

		if s.statusProvider != nil {
			s.statusProvider.SetStopped(gameserverID)
		}
		s.broadcaster.Publish(controller.LifecycleEvent{Type_: controller.EventInstanceStopping, GameserverID: gameserverID, Timestamp: time.Now()})

		if err := s.doStop(ctx, gameserverID); err != nil {
			s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
			return fmt.Errorf("stopping gameserver for migration: %w", err)
		}
	}

	// Re-fetch source worker — may have gone offline during stop
	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", fmt.Errorf("source worker offline")), Timestamp: time.Now()})
		return fmt.Errorf("source worker went offline during migration")
	}

	targetWorker, err := s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil || targetWorker == nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", fmt.Errorf("target worker offline")), Timestamp: time.Now()})
		return fmt.Errorf("target worker went offline during migration")
	}

	// Transfer volume via backup store (avoids buffering entire volume in controller memory)
	s.log.Info("transferring volume data via store", "gameserver", gameserverID, "volume", gs.VolumeName)
	migrationID := uuid.New().String()

	// Tar + gzip from source -> store
	tarReader, err := sourceWorker.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
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
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("saving migration data to store: %w", err)
	}
	if compressErr != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", compressErr), Timestamp: time.Now()})
		return fmt.Errorf("compressing volume data: %w", compressErr)
	}
	s.log.Info("migration data stored", "gameserver", gameserverID, "migration", migrationID)

	// Store -> restore on target
	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("creating volume on target worker: %w", err)
	}

	reader, err := s.backupStore.Load(ctx, "migrations", migrationID)
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("loading migration data from store: %w", err)
	}
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("decompressing migration data: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up target volume after failed restore", "volume", gs.VolumeName, "error", rmErr)
		}
		s.log.Error("migration restore failed, data preserved in store", "migration", migrationID)
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("restoring volume on target worker: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Verify the target volume actually has data before deleting anything.
	// A silent restore failure would leave an empty volume — without this check
	// we'd delete the source volume and lose the data permanently.
	targetSize, err := targetWorker.VolumeSize(ctx, gs.VolumeName)
	if err != nil {
		s.log.Error("migration: failed to verify target volume, aborting — source volume preserved", "migration", migrationID, "error", err)
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		return fmt.Errorf("verifying target volume after restore: %w", err)
	}
	if targetSize == 0 {
		s.log.Error("migration: target volume is empty after restore, aborting — source volume preserved", "migration", migrationID, "volume", gs.VolumeName)
		emptyErr := fmt.Errorf("target volume is empty after restore — data may not have transferred. Source volume preserved on %s, migration data preserved in store as %s", currentNodeID, migrationID)
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", emptyErr), Timestamp: time.Now()})
		return emptyErr
	}
	s.log.Info("migration restore verified", "gameserver", gameserverID, "target_volume_bytes", targetSize)

	// Update node assignment (and reallocate ports if using per-node port scope)
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

	// Source volume is safe to delete — target is verified
	if err := sourceWorker.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove old volume from source worker", "volume", gs.VolumeName, "error", err)
	}

	// Clean up migration data only after source is removed — it's the last safety net
	if err := s.backupStore.Delete(ctx, "migrations", migrationID); err != nil {
		s.log.Warn("failed to clean up migration data from store", "migration", migrationID, "error", err)
	}

	s.log.Info("gameserver migrated", "gameserver", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)

	if wasRunning {
		s.log.Info("restarting gameserver after migration", "gameserver", gameserverID)
		if err := s.doStart(ctx, gameserverID); err != nil {
			s.log.Error("failed to restart gameserver after migration", "gameserver", gameserverID, "error", err)
			// Migration succeeded but restart failed — don't return error, data is safe
			s.broadcaster.Publish(controller.LifecycleEvent{Type_: controller.EventInstanceStopped, GameserverID: gameserverID, Timestamp: time.Now()})
		}
	} else {
		s.broadcaster.Publish(controller.LifecycleEvent{Type_: controller.EventInstanceStopped, GameserverID: gameserverID, Timestamp: time.Now()})
	}

	return nil
}
