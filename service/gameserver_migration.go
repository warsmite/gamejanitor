package service

import (
	"github.com/warsmite/gamejanitor/controller"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/warsmite/gamejanitor/model"
	"github.com/google/uuid"
)

// MigrateGameserver moves a gameserver from its current node to a different node.
// Requires both source and target workers to be online.
func (s *GameserverService) MigrateGameserver(ctx context.Context, gameserverID string, targetNodeID string) (err error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
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
	targetWorker, err := s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return controller.ErrUnavailablef("target worker unavailable: %v", err)
	}

	// Validate target node labels
	if !gs.NodeTags.IsEmpty() {
		targetNode, err := model.GetWorkerNode(s.db, targetNodeID)
		if err != nil || targetNode == nil {
			return controller.ErrNotFoundf("target node %s not found", targetNodeID)
		}
		if !targetNode.Tags.HasAll(gs.NodeTags) {
			return controller.ErrBadRequestf("target node %s missing required labels: %v", targetNodeID, gs.NodeTags)
		}
	}

	// Check target node limits
	if err := s.checkWorkerLimits(targetNodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
		return err
	}

	// Get source worker (must be online to transfer data)
	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		return controller.ErrUnavailable("source worker is offline, cannot migrate (both workers must be online)")
	}

	s.log.Info("migrating gameserver", "id", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverMigrate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gameserverID,
		Gameserver:   gs,
	})

	defer func() {
		if err != nil {
			s.broadcaster.Publish(GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Migration failed", err), Timestamp: time.Now()})
		}
	}()

	// Stop if running
	if gs.Status != controller.StatusStopped {
		s.log.Info("stopping gameserver for migration", "id", gameserverID)
		if err := s.Stop(ctx, gameserverID); err != nil {
			return fmt.Errorf("stopping gameserver for migration: %w", err)
		}
	}

	// Transfer volume via backup store (avoids buffering entire volume in controller memory)
	s.log.Info("transferring volume data via store", "id", gameserverID, "volume", gs.VolumeName)
	migrationID := uuid.New().String()

	// Tar + gzip from source → store
	tarReader, err := sourceWorker.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
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
		gzWriter.Close()
		pw.Close()
	}()

	if err := s.store.Save(ctx, "migrations", migrationID, pr); err != nil {
		return fmt.Errorf("saving migration data to store: %w", err)
	}
	if compressErr != nil {
		return fmt.Errorf("compressing volume data: %w", compressErr)
	}
	s.log.Info("migration data stored", "id", gameserverID, "migration_id", migrationID)

	// Store → restore on target
	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("creating volume on target worker: %w", err)
	}

	reader, err := s.store.Load(ctx, "migrations", migrationID)
	if err != nil {
		return fmt.Errorf("loading migration data from store: %w", err)
	}
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		return fmt.Errorf("decompressing migration data: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up target volume after failed restore", "volume", gs.VolumeName, "error", rmErr)
		}
		// Don't delete migration data on failure — operator can retry manually
		s.log.Error("migration restore failed, data preserved in store", "migration_id", migrationID)
		return fmt.Errorf("restoring volume on target worker: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Cleanup migration data on success
	if err := s.store.Delete(ctx, "migrations", migrationID); err != nil {
		s.log.Warn("failed to clean up migration data from store", "migration_id", migrationID, "error", err)
	}

	// Update node assignment (and reallocate ports if using per-node port scope)
	gs.NodeID = &targetNodeID

	if s.settingsSvc.GetString(SettingPortUniqueness) == "node" {
		s.placementMu.Lock()
		game := s.gameStore.GetGame(gs.GameID)
		if game == nil {
			s.placementMu.Unlock()
			return controller.ErrNotFoundf("game %s not found", gs.GameID)
		}
		newPorts, err := s.AllocatePorts(game, targetNodeID, "")
		if err != nil {
			s.placementMu.Unlock()
			return fmt.Errorf("allocating ports on target node: %w", err)
		}
		gs.Ports = newPorts
		s.placementMu.Unlock()
	}

	if err := model.UpdateGameserver(s.db, gs); err != nil {
		return fmt.Errorf("updating gameserver node assignment: %w", err)
	}

	// Clean up old volume on source worker
	if err := sourceWorker.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove old volume from source worker", "volume", gs.VolumeName, "error", err)
	}

	s.broadcaster.Publish(ContainerStoppedEvent{GameserverID: gameserverID, Timestamp: time.Now()})
	s.log.Info("gameserver migrated", "id", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)
	return nil
}
