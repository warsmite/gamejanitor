package gameserver

import (
	"context"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/utilities/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// RecoverOnStartup reconciles DB status with runtime state.
// Any gameserver not in a terminal state (stopped/error) is checked against
// the actual instance and corrected.
func (m *StatusManager) RecoverOnStartup(ctx context.Context) error {
	m.log.Info("recovering gameserver status from instance state")

	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		return err
	}

	var withInstance, instanceMissing int

	for _, gs := range gameservers {
		w := m.workerForGameserver(&gs)
		if w == nil {
			// Worker is offline — DeriveStatus will return "unreachable"
			if gs.NodeID != nil {
				m.log.Warn("worker offline at startup, gameserver will show unreachable", "gameserver", gs.ID, "node_id", *gs.NodeID)
			}
			continue
		}

		if gs.InstanceID != nil {
			withInstance++
		}
		if m.recoverGameserver(ctx, &gs, w) {
			instanceMissing++
		}
	}

	if withInstance > 0 && instanceMissing == withInstance {
		m.log.Warn("all gameserver instances are missing — did you switch runtimes? Volumes may need manual migration",
			"expected_instances", withInstance,
		)
	}

	return nil
}

// workerForGameserver returns the appropriate worker, or nil if unavailable.
func (m *StatusManager) workerForGameserver(gs *model.Gameserver) worker.Worker {
	return m.dispatcher.WorkerFor(gs.ID)
}

// recoverGameserver reconciles runtime state with instance reality on a worker.
// Returns true if the gameserver had an instance ID but the instance was not found.
func (m *StatusManager) recoverGameserver(ctx context.Context, gs *model.Gameserver, w worker.Worker) bool {
	if gs.InstanceID == nil {
		m.log.Info("gameserver has no instance, state is stopped", "gameserver", gs.ID)
		m.workerStateMu.Lock()
		delete(m.workerStates, gs.ID)
		m.workerStateMu.Unlock()
		return false
	}

	info, err := w.InspectInstance(ctx, *gs.InstanceID)
	if err != nil {
		m.log.Warn("instance not found, clearing", "gameserver", gs.ID, "instance_id", truncID(*gs.InstanceID), "error", err)
		gs.InstanceID = nil
		gs.DesiredState = "stopped"
		m.store.UpdateGameserver(gs)
		m.workerStateMu.Lock()
		delete(m.workerStates, gs.ID)
		m.workerStateMu.Unlock()
		return true
	}

	switch info.State {
	case "running":
		m.log.Info("instance running, populating worker state cache", "gameserver", gs.ID)
		m.workerStateMu.Lock()
		m.workerStates[gs.ID] = &worker.InstanceStateUpdate{
			InstanceID: *gs.InstanceID,
			State:      worker.StateRunning,
			StartedAt:  info.StartedAt,
		}
		m.workerStateMu.Unlock()
		m.startPolling(gs.ID)
	case "exited", "dead", "created":
		m.log.Info("instance is not running, clearing", "gameserver", gs.ID, "state", info.State)
		gs.InstanceID = nil
		gs.DesiredState = "stopped"
		m.store.UpdateGameserver(gs)
		m.workerStateMu.Lock()
		delete(m.workerStates, gs.ID)
		m.workerStateMu.Unlock()
	default:
		m.log.Warn("instance in unexpected state", "gameserver", gs.ID, "state", info.State)
		m.workerStateMu.Lock()
		delete(m.workerStates, gs.ID)
		m.workerStateMu.Unlock()
	}
	return false
}

// watchWorkerEvents starts a goroutine that watches instance state updates from a worker.
func (m *StatusManager) watchWorkerEvents(ctx context.Context, label string, w worker.Worker) {
	updateCh, errCh := w.WatchInstanceStates(ctx)

	go func() {
		m.log.Debug("watching instance states", "worker", label)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errCh:
				if !ok {
					return
				}
				m.log.Warn("instance state watcher stream broke, forcing worker re-registration on next heartbeat", "worker", label, "error", err)
				// Clear the worker from the registry so the next heartbeat
				// falls through to the dial-back path and re-establishes both
				// the gRPC connection and the event stream. Don't use SetOffline
				// — gameservers are still running, we just lost the event stream.
				m.registry.ClearWorker(label)
				return
			case update, ok := <-updateCh:
				if !ok {
					return
				}
				m.handleInstanceStateUpdate(update)
			}
		}
	}()
}

func (m *StatusManager) handleInstanceStateUpdate(update worker.InstanceStateUpdate) {
	gsID, ok := naming.GameserverIDFromInstanceName(update.InstanceName)
	if !ok {
		return
	}

	gs, err := m.store.GetGameserver(gsID)
	if err != nil || gs == nil {
		m.log.Debug("instance state update for unknown gameserver", "instance_name", update.InstanceName, "state", update.State)
		return
	}

	// Ignore stale events from instances that no longer match the current gameserver.
	// After Stop(), InstanceID is nil — any late "running" event from the old instance
	// should not re-populate worker state.
	if gs.InstanceID == nil || *gs.InstanceID != update.InstanceID {
		m.log.Debug("instance state: ignoring stale update", "gameserver", gsID, "event_instance", truncID(update.InstanceID), "gs_instance", gs.InstanceID, "state", update.State)
		return
	}

	// Capture previous state before update
	m.workerStateMu.Lock()
	prev := m.workerStates[gsID]
	m.workerStateMu.Unlock()

	switch update.State {
	case worker.StateRunning:
		m.workerStateMu.Lock()
		m.workerStates[gsID] = &update
		delete(m.errorReasons, gsID)
		m.workerStateMu.Unlock()

		m.log.Info("instance ready", "gameserver", gsID)
		m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverReady, gsID, nil))
		m.startPolling(gsID)

		// Publish status change
		m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, gsID, &event.StatusChangedData{
			Status: controller.StatusRunning,
		}))

		// Persist install flag
		if update.Installed && !gs.Installed {
			gs.Installed = true
			if err := m.store.UpdateGameserver(gs); err != nil {
				m.log.Error("failed to mark installed", "gameserver", gsID, "error", err)
			}
		}

	case worker.StateExited:
		m.stopPolling(gsID)

		// If previous state was running/starting, this is an unexpected death.
		// If prev is nil, SetStopped was called before the exit event — this is
		// an expected stop. Don't write the exit state to the cache, because
		// DeriveStatus would see the non-zero exit code and return "error" during
		// the window before stopInstance finishes its cleanup.
		wasRunning := prev != nil && (prev.State == worker.StateRunning || prev.State == worker.StateStarting)
		if wasRunning {
			m.workerStateMu.Lock()
			m.workerStates[gsID] = &update
			m.workerStateMu.Unlock()

			reason := describeExit(update.ExitCode, time.Since(update.StartedAt), m.statsPoller.GetCachedStats(gsID))
			m.log.Warn("unexpected instance death", "gameserver", gsID, "exit_code", update.ExitCode, "reason", reason)
			m.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceExited, gsID, nil))
			m.handleUnexpectedDeath(gs, reason)

			m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, gsID, &event.StatusChangedData{
				Status:      controller.StatusError,
				ErrorReason: reason,
			}))
		} else {
			// Expected stop — don't write exit state to cache
			m.log.Debug("instance state: expected instance stop", "gameserver", gsID)
		}
	}
}

// onWorkerRegistered is called when a remote worker registers.
// Starts event watching and recovers gameservers on that worker.
func (m *StatusManager) onWorkerRegistered(nodeID string, w worker.Worker) {
	m.workerMu.Lock()

	// Cancel existing watcher if re-registering
	if cancel, ok := m.workerCancels[nodeID]; ok {
		cancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.workerCancels[nodeID] = cancel
	m.workerMu.Unlock()

	m.log.Info("starting event watcher for remote worker", "worker", nodeID)
	m.watchWorkerEvents(ctx, nodeID, w)

	m.broadcaster.Publish(event.NewEvent(event.EventWorkerConnected, "", event.SystemActor, &event.WorkerActionData{
		WorkerID: nodeID,
	}))

	// Recover gameservers on this worker
	go m.recoverWorkerGameservers(ctx, nodeID, w)
}

// onWorkerOffline is called when a worker transitions to offline (heartbeat timeout or explicit).
// Marks affected gameservers as unreachable so the UI doesn't show stale "running" status.
func (m *StatusManager) onWorkerOffline(nodeID string) {
	// DeriveStatus will return "unreachable" for gameservers on this worker
	// since the registry no longer reports it as online. No DB write needed.

	m.workerMu.Lock()
	if cancel, ok := m.workerCancels[nodeID]; ok {
		cancel()
		delete(m.workerCancels, nodeID)
	}
	m.workerMu.Unlock()

	m.broadcaster.Publish(event.NewEvent(event.EventWorkerDisconnected, "", event.SystemActor, &event.WorkerActionData{
		WorkerID: nodeID,
	}))

	m.log.Info("stopped event watcher for disconnected worker", "worker", nodeID)
}

// recoverWorkerGameservers recovers gameservers assigned to a specific worker node
// and detects orphan instances (running on the worker but not tracked in DB).
func (m *StatusManager) recoverWorkerGameservers(ctx context.Context, nodeID string, w worker.Worker) {
	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		m.log.Error("failed to list gameservers for worker recovery", "worker", nodeID, "error", err)
		return
	}

	// Forward check: DB → runtime (existing recovery)
	knownIDs := make(map[string]bool)
	for _, gs := range gameservers {
		if gs.NodeID == nil || *gs.NodeID != nodeID {
			continue
		}
		knownIDs[gs.ID] = true
		if gs.InstanceID == nil {
			continue
		}
		m.log.Info("recovering gameserver on reconnected worker", "gameserver", gs.ID, "worker", nodeID)
		m.recoverGameserver(ctx, &gs, w)
	}

	// Reverse check: runtime → DB (orphan detection)
	m.detectOrphanInstances(ctx, nodeID, w, knownIDs)
}

// detectOrphanInstances finds gamejanitor instances running on a worker that
// aren't tracked in the database. These are logged as warnings — not auto-removed,
// as they may contain player data (e.g. after a DB restore).
func (m *StatusManager) detectOrphanInstances(ctx context.Context, nodeID string, w worker.Worker, knownIDs map[string]bool) {
	instances, err := w.ListGameserverInstances(ctx)
	if err != nil {
		m.log.Warn("failed to list instances for orphan detection", "worker", nodeID, "error", err)
		return
	}

	for _, c := range instances {
		if knownIDs[c.GameserverID] {
			continue
		}
		// Also check gameservers on other nodes (might have been migrated)
		gs, _ := m.store.GetGameserver(c.GameserverID)
		if gs != nil {
			continue
		}
		m.log.Warn("orphan instance detected — instance exists on worker but gameserver not found in database",
			"worker", nodeID, "instance_id", truncID(c.InstanceID), "instance_name", c.InstanceName,
			"gameserver", c.GameserverID, "state", c.State)
	}
}
