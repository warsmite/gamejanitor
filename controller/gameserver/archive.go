package gameserver

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/util/naming"
)

// Archive stops the gameserver, backs up its volume to archive storage,
// removes the instance and volume from the worker, and marks it as archived.
func (g *LiveGameserver) Archive(ctx context.Context) error {
	g.mu.Lock()
	if g.spec.DesiredState == model.DesiredArchived {
		g.mu.Unlock()
		return controller.ErrConflictf("gameserver %s is already archived", g.spec.ID)
	}
	if g.backupStore == nil {
		g.mu.Unlock()
		return controller.ErrBadRequest("backup storage is not configured, cannot archive")
	}
	g.mu.Unlock()

	return g.submitOperation(operationOpts{
		opType:         model.OpArchive,
		initialPhase:   model.PhaseStopping,
		requireWorker:  true,
		errorPrefix:    "Archive failed",
		clearOnSuccess: true, // Archive is terminal — no running state to wait for
	}, g.executeArchive)
}

func (g *LiveGameserver) executeArchive(ctx context.Context) error {
	// Stop if running
	if err := g.stopIfRunning(ctx); err != nil {
		return fmt.Errorf("stopping gameserver before archive: %w", err)
	}

	g.setPhase(model.PhaseCreatingBackup)

	g.mu.Lock()
	w := g.worker
	volumeName := g.spec.VolumeName
	instanceID := g.spec.InstanceID
	g.mu.Unlock()

	if w == nil {
		return fmt.Errorf("worker unavailable, cannot archive")
	}

	// Back up volume to archive storage
	tarReader, err := w.BackupVolume(ctx, volumeName)
	if err != nil {
		return fmt.Errorf("backing up volume: %w", err)
	}

	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = fmt.Errorf("compressing archive data: %w", err)
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

	if err := g.backupStore.SaveArchive(ctx, g.spec.ID, pr); err != nil {
		return fmt.Errorf("saving archive to store: %w", err)
	}
	if compressErr != nil {
		g.backupStore.DeleteArchive(ctx, g.spec.ID)
		return compressErr
	}

	// Remove instance if it exists
	if instanceID != nil {
		if err := w.RemoveInstance(ctx, *instanceID); err != nil {
			g.log.Warn("failed to remove instance during archive", "instance_id", *instanceID, "error", err)
		}
	}
	// Also try by name in case instance_id is stale
	instanceName := naming.InstanceName(g.spec.ID)
	if err := w.RemoveInstance(ctx, instanceName); err != nil {
		g.log.Debug("no instance to remove by name during archive", "name", instanceName)
	}

	// Remove volume from worker
	if err := w.RemoveVolume(ctx, volumeName); err != nil {
		g.log.Warn("failed to remove volume during archive", "volume", volumeName, "error", err)
	}

	// Update state to archived
	g.mu.Lock()
	g.spec.DesiredState = model.DesiredArchived
	g.spec.InstanceID = nil
	g.spec.NodeID = nil
	g.worker = nil
	g.clearProcessLocked()
	g.spec.ErrorReason = ""
	g.mu.Unlock()

	// Persist to DB
	dbGS, err := g.store.GetGameserver(g.spec.ID)
	if err != nil {
		return fmt.Errorf("loading gameserver from DB for archive update: %w", err)
	}
	if dbGS == nil {
		return fmt.Errorf("gameserver %s not found in DB", g.spec.ID)
	}
	dbGS.DesiredState = model.DesiredArchived
	dbGS.InstanceID = nil
	dbGS.NodeID = nil
	dbGS.ErrorReason = ""
	if err := g.store.UpdateGameserver(dbGS); err != nil {
		return fmt.Errorf("persisting archive state: %w", err)
	}

	g.log.Info("gameserver archived")
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverArchive, g.spec.ID, nil))

	return nil
}

// Unarchive restores a gameserver from archive storage onto a target node.
// If targetNodeID is empty, auto-selects via placement ranking.
func (g *LiveGameserver) Unarchive(ctx context.Context, targetNodeID string) error {
	g.mu.Lock()
	if g.spec.DesiredState != model.DesiredArchived {
		g.mu.Unlock()
		return controller.ErrConflictf("gameserver %s is not archived", g.spec.ID)
	}
	if g.backupStore == nil {
		g.mu.Unlock()
		return controller.ErrBadRequest("backup storage is not configured, cannot unarchive")
	}
	g.mu.Unlock()

	// requireWorker=false because unarchive ASSIGNS a worker (the gameserver
	// starts with no node, unarchive picks one).
	return g.submitOperation(operationOpts{
		opType:         model.OpUnarchive,
		initialPhase:   model.PhaseRestoringBackup,
		requireWorker:  false,
		errorPrefix:    "Unarchive failed",
		clearOnSuccess: true, // Unarchive leaves the gameserver stopped, not running
	}, func(ctx context.Context) error {
		return g.executeUnarchive(ctx, targetNodeID)
	})
}

func (g *LiveGameserver) executeUnarchive(ctx context.Context, targetNodeID string) error {
	// Select target node
	if targetNodeID == "" {
		g.mu.Lock()
		nodeTags := g.spec.NodeTags
		g.mu.Unlock()

		candidates := g.dispatcher.RankWorkersForPlacement(nodeTags)
		if len(candidates) == 0 {
			return fmt.Errorf("no workers available for unarchive placement")
		}
		targetNodeID = candidates[0].NodeID
		g.log.Info("auto-selected node for unarchive", "node_id", targetNodeID)
	}

	targetWorker, err := g.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return fmt.Errorf("target worker unavailable: %w", err)
	}

	g.mu.Lock()
	volumeName := g.spec.VolumeName
	g.mu.Unlock()

	// Create volume on target
	if err := targetWorker.CreateVolume(ctx, volumeName); err != nil {
		return fmt.Errorf("creating volume on target: %w", err)
	}

	// Load archive from store and decompress into volume
	reader, err := g.backupStore.LoadArchive(ctx, g.spec.ID)
	if err != nil {
		return fmt.Errorf("loading archive from store: %w", err)
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		return fmt.Errorf("decompressing archive: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, volumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		return fmt.Errorf("restoring volume from archive: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Reallocate ports if port_uniqueness is "node" (ports may conflict on new node)
	g.mu.Lock()
	gameID := g.spec.GameID
	gsID := g.spec.ID
	g.mu.Unlock()

	if g.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		game := g.gameStore.GetGame(gameID)
		if game != nil {
			newPorts, err := g.placement.ReallocatePorts(game, targetNodeID, gsID)
			if err != nil {
				g.log.Warn("failed to reallocate ports during unarchive, keeping existing", "error", err)
			} else if newPorts != nil {
				g.mu.Lock()
				g.spec.Ports = newPorts
				g.mu.Unlock()
			}
		}
	}

	// Update state
	g.mu.Lock()
	g.spec.DesiredState = model.DesiredStopped
	g.spec.NodeID = &targetNodeID
	g.worker = targetWorker
	g.spec.ErrorReason = ""
	g.mu.Unlock()

	// Persist to DB
	dbGS, err := g.store.GetGameserver(g.spec.ID)
	if err != nil {
		return fmt.Errorf("loading gameserver from DB for unarchive update: %w", err)
	}
	if dbGS == nil {
		return fmt.Errorf("gameserver %s not found in DB", g.spec.ID)
	}
	dbGS.DesiredState = model.DesiredStopped
	dbGS.NodeID = &targetNodeID
	dbGS.ErrorReason = ""

	g.mu.Lock()
	dbGS.Ports = g.spec.Ports
	g.mu.Unlock()

	if err := g.store.UpdateGameserver(dbGS); err != nil {
		return fmt.Errorf("persisting unarchive state: %w", err)
	}

	g.log.Info("gameserver unarchived", "node_id", targetNodeID)
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverUnarchive, g.spec.ID, nil))

	return nil
}

// Migrate moves a gameserver from its current node to a target node. The volume
// is backed up from the source, restored on the target, and then removed from
// the source. If the gameserver was running, it is restarted on the target.
func (g *LiveGameserver) Migrate(ctx context.Context, targetNodeID string) error {
	// Synchronous pre-validation before submitting the operation.
	g.mu.Lock()
	currentNodeID := ""
	if g.spec.NodeID != nil {
		currentNodeID = *g.spec.NodeID
	}
	if currentNodeID == targetNodeID {
		g.mu.Unlock()
		return controller.ErrBadRequestf("gameserver is already on node %s", targetNodeID)
	}
	if currentNodeID == "" {
		g.mu.Unlock()
		return controller.ErrBadRequest("gameserver has no current node, cannot migrate")
	}
	memoryNeeded := g.spec.MemoryLimitMB
	cpuNeeded := g.spec.CPULimit
	storageNeeded := ptrIntOr0(g.spec.StorageLimitMB)
	nodeTags := g.spec.NodeTags
	g.mu.Unlock()

	if _, err := g.dispatcher.SelectWorkerByNodeID(targetNodeID); err != nil {
		return controller.ErrUnavailablef("target worker unavailable: %v", err)
	}
	if !nodeTags.IsEmpty() {
		targetNode, err := g.store.GetWorkerNode(targetNodeID)
		if err != nil || targetNode == nil {
			return controller.ErrNotFoundf("target node %s not found", targetNodeID)
		}
		if !targetNode.Tags.HasAll(nodeTags) {
			return controller.ErrBadRequestf("target node %s missing required tags: %v", targetNodeID, nodeTags)
		}
	}
	if err := g.placement.CheckWorkerLimitsExcluding(targetNodeID, memoryNeeded, cpuNeeded, storageNeeded, g.spec.ID); err != nil {
		return err
	}
	if g.dispatcher.WorkerFor(g.spec.ID) == nil {
		return controller.ErrUnavailable("source worker is offline, cannot migrate")
	}

	// clearOnSuccess=false: Migrate may end in either "running" (wasRunning=true,
	// HandleProcessEvent clears the operation) or "stopped" (wasRunning=false,
	// we need to clear here). executeMigrate returns nil in both cases; we
	// inspect state afterward to decide whether to clear.
	return g.submitOperation(operationOpts{
		opType:         model.OpMigrate,
		initialPhase:   model.PhaseStopping,
		requireWorker:  false, // source worker was checked above; executeMigrate validates again
		errorPrefix:    "Migration failed",
		clearOnSuccess: false,
	}, func(ctx context.Context) error {
		if err := g.executeMigrate(ctx, targetNodeID); err != nil {
			return err
		}
		// On success: if the gameserver is not running, clear the operation
		// (migrate-stopped path). If it is running, HandleProcessEvent cleared it.
		g.mu.Lock()
		if g.operation != nil && g.processState == model.ProcessNone {
			g.operation = nil
			g.notifyWatchers(nil)
		}
		g.mu.Unlock()
		return nil
	})
}

func (g *LiveGameserver) executeMigrate(ctx context.Context, targetNodeID string) error {
	// Record whether the gameserver was running so we can restart after migration
	g.mu.Lock()
	wasRunning := g.processState == model.ProcessRunning
	volumeName := g.spec.VolumeName
	gameID := g.spec.GameID
	g.mu.Unlock()

	// Stop if running
	if err := g.stopIfRunning(ctx); err != nil {
		return fmt.Errorf("stopping gameserver before migration: %w", err)
	}

	g.setPhase(model.PhaseMigrating)

	sourceWorker := g.dispatcher.WorkerFor(g.spec.ID)
	if sourceWorker == nil {
		return fmt.Errorf("source worker went offline during migration")
	}

	targetWorker, err := g.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return fmt.Errorf("target worker unavailable: %w", err)
	}

	// Back up volume from source
	migrationID := uuid.New().String()
	tarReader, err := sourceWorker.BackupVolume(ctx, volumeName)
	if err != nil {
		return fmt.Errorf("backing up volume from source: %w", err)
	}

	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = fmt.Errorf("compressing migration data: %w", err)
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

	// Store migration data temporarily via the backup store
	if err := g.backupStore.Save(ctx, g.spec.ID, migrationID, pr); err != nil {
		return fmt.Errorf("saving migration data: %w", err)
	}
	if compressErr != nil {
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return compressErr
	}

	// Create volume on target and restore
	if err := targetWorker.CreateVolume(ctx, volumeName); err != nil {
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return fmt.Errorf("creating volume on target: %w", err)
	}

	reader, err := g.backupStore.Load(ctx, g.spec.ID, migrationID)
	if err != nil {
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return fmt.Errorf("loading migration data: %w", err)
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return fmt.Errorf("decompressing migration data: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, volumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return fmt.Errorf("restoring volume on target: %w", err)
	}
	gzReader.Close()
	reader.Close()

	// Verify the target volume has data
	targetSize, err := targetWorker.VolumeSize(ctx, volumeName)
	if err != nil {
		g.log.Warn("failed to check target volume size after migration", "error", err)
	} else if targetSize == 0 {
		g.backupStore.Delete(ctx, g.spec.ID, migrationID)
		return fmt.Errorf("target volume is empty after restore, aborting migration")
	}

	// Reallocate ports if port_uniqueness is "node"
	if g.settingsSvc.GetString(settings.SettingPortUniqueness) == "node" {
		game := g.gameStore.GetGame(gameID)
		if game != nil {
			newPorts, err := g.placement.ReallocatePorts(game, targetNodeID, g.spec.ID)
			if err != nil {
				g.log.Warn("failed to reallocate ports during migration, keeping existing", "error", err)
			} else if newPorts != nil {
				g.mu.Lock()
				g.spec.Ports = newPorts
				g.mu.Unlock()
			}
		}
	}

	// Update state to target node
	g.mu.Lock()
	g.spec.NodeID = &targetNodeID
	g.worker = targetWorker
	g.mu.Unlock()

	// Persist to DB
	dbGS, err := g.store.GetGameserver(g.spec.ID)
	if err != nil {
		return fmt.Errorf("loading gameserver from DB for migration update: %w", err)
	}
	if dbGS == nil {
		return fmt.Errorf("gameserver %s not found in DB", g.spec.ID)
	}
	dbGS.NodeID = &targetNodeID

	g.mu.Lock()
	dbGS.Ports = g.spec.Ports
	g.mu.Unlock()

	if err := g.store.UpdateGameserver(dbGS); err != nil {
		return fmt.Errorf("persisting migration state: %w", err)
	}

	// Clean up: remove volume from source, delete migration data
	if err := sourceWorker.RemoveVolume(ctx, volumeName); err != nil {
		g.log.Warn("failed to remove volume from source after migration", "volume", volumeName, "error", err)
	}
	if err := g.backupStore.Delete(ctx, g.spec.ID, migrationID); err != nil {
		g.log.Warn("failed to clean up migration data", "migration_id", migrationID, "error", err)
	}

	g.log.Info("gameserver migrated", "target_node_id", targetNodeID)
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverMigrate, g.spec.ID, nil))

	// Restart on target if it was running before migration.
	// Call executeStart directly — we're already inside the migrate operation goroutine.
	// HandleProcessEvent will clear the operation when the worker reports ready.
	if wasRunning {
		g.log.Info("restarting gameserver on target after migration")
		if err := g.executeStart(ctx); err != nil {
			g.log.Error("failed to restart after migration", "error", err)
			g.bus.Publish(event.NewSystemEvent(event.EventGameserverError, g.spec.ID, &event.ErrorData{
				Reason: fmt.Sprintf("Restart after migration failed: %v", err),
			}))
			return err
		}
		return nil // operation cleared by HandleProcessEvent
	}

	return nil
}

// stopIfRunning stops the gameserver if it has a running instance.
// Returns nil if the gameserver is already stopped or has no instance.
func (g *LiveGameserver) stopIfRunning(ctx context.Context) error {
	g.mu.Lock()
	w := g.worker
	instanceID := g.spec.InstanceID
	g.mu.Unlock()

	if w == nil || instanceID == nil {
		return nil
	}

	g.stopInstanceOnWorker(ctx, w, *instanceID)

	g.mu.Lock()
	g.spec.InstanceID = nil
	g.clearProcessLocked()
	g.mu.Unlock()

	g.store.SetInstanceID(g.spec.ID, nil)

	return nil
}

func ptrIntOr0(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}
