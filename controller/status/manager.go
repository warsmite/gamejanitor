package status

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

type StatusManager struct {
	store       Store
	log         *slog.Logger
	broadcaster *controller.EventBus
	querySvc    *QueryService
	statsPoller *StatsPoller
	dispatcher  *orchestrator.Dispatcher
	registry    *orchestrator.Registry
	restartFunc func(ctx context.Context, id string) error

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Worker-reported state: the source of truth for instance lifecycle
	workerStateMu sync.RWMutex
	workerStates  map[string]*worker.InstanceStateUpdate // gameserverID → last worker report
	errorReasons  map[string]string                      // gameserverID → controller-side error reason

	// Per-worker event watchers for multi-node
	workerCancels map[string]context.CancelFunc
	workerMu      sync.Mutex

	// Auto-restart crash counter: reset when gameserver reaches "running"
	crashCounts map[string]int
	crashMu     sync.Mutex
}

func NewStatusManager(store Store, broadcaster *controller.EventBus, querySvc *QueryService, statsPoller *StatsPoller, dispatcher *orchestrator.Dispatcher, registry *orchestrator.Registry, restartFunc func(ctx context.Context, id string) error, log *slog.Logger) *StatusManager {
	sm := &StatusManager{
		store:         store,
		broadcaster:   broadcaster,
		querySvc:      querySvc,
		statsPoller:   statsPoller,
		dispatcher:    dispatcher,
		registry:      registry,
		restartFunc:   restartFunc,
		log:           log,
		workerStates:  make(map[string]*worker.InstanceStateUpdate),
		errorReasons:  make(map[string]string),
		workerCancels: make(map[string]context.CancelFunc),
		crashCounts:   make(map[string]int),
	}

	registry.SetCallbacks(sm.onWorkerRegistered, sm.onWorkerOffline)

	return sm
}

// Start begins watching for status events.
// Workers are watched via registry callbacks (onWorkerRegistered).
func (m *StatusManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	// Listen for error events from lifecycle code
	events, unsub := m.broadcaster.Subscribe()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				switch e := ev.(type) {
				case controller.GameserverErrorEvent:
					m.log.Warn("gameserver error event", "gameserver", e.GameserverID, "reason", e.Reason)
					m.workerStateMu.Lock()
					m.errorReasons[e.GameserverID] = e.Reason
					m.workerStateMu.Unlock()
					m.stopPolling(e.GameserverID)
				}
			}
		}
	}()

	m.log.Info("status manager started")
}

func (m *StatusManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}

	// Stop all remote worker watchers
	m.workerMu.Lock()
	for id, cancel := range m.workerCancels {
		cancel()
		delete(m.workerCancels, id)
	}
	m.workerMu.Unlock()

	m.wg.Wait()
	m.log.Info("status manager stopped")
}

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
				m.log.Error("instance state watcher error", "worker", label, "error", err)
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
	m.workerStates[gsID] = &update
	m.workerStateMu.Unlock()

	// Publish status change so SSE/webhook consumers get the derived display status
	newStatus, newReason := m.DeriveStatus(gs)
	m.broadcaster.Publish(controller.GameserverStatusChangedEvent{
		GameserverID: gsID,
		Status:       newStatus,
		ErrorReason:  newReason,
		Timestamp:    time.Now(),
	})

	switch update.State {
	case worker.StateRunning:
		m.log.Info("instance ready", "gameserver", gsID)
		m.broadcaster.Publish(controller.GameserverReadyEvent{GameserverID: gsID, Timestamp: time.Now()})
		m.startPolling(gsID)

		// Clear error state on successful start
		m.workerStateMu.Lock()
		delete(m.errorReasons, gsID)
		m.workerStateMu.Unlock()

		// Persist install flag
		if update.Installed && !gs.Installed {
			gs.Installed = true
			if err := m.store.UpdateGameserver(gs); err != nil {
				m.log.Error("failed to mark installed", "gameserver", gsID, "error", err)
			}
		}

	case worker.StateExited:
		m.stopPolling(gsID)

		// If previous state was running/starting, this is an unexpected death
		wasRunning := prev != nil && (prev.State == worker.StateRunning || prev.State == worker.StateStarting)
		if wasRunning {
			reason := describeExit(update.ExitCode, time.Since(update.StartedAt), m.statsPoller.GetCachedStats(gsID))
			m.log.Warn("unexpected instance death", "gameserver", gsID, "exit_code", update.ExitCode, "reason", reason)
			m.broadcaster.Publish(controller.InstanceExitedEvent{GameserverID: gsID, Timestamp: time.Now()})
			m.handleUnexpectedDeath(gs, reason)
		} else {
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

	m.broadcaster.Publish(controller.WorkerActionEvent{
		Type:      controller.EventWorkerConnected,
		Timestamp: time.Now(),
		Actor:     controller.SystemActor,
		WorkerID:  nodeID,
	})

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

	m.broadcaster.Publish(controller.WorkerActionEvent{
		Type:      controller.EventWorkerDisconnected,
		Timestamp: time.Now(),
		Actor:     controller.SystemActor,
		WorkerID:  nodeID,
	})

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

// --- Runtime state methods ---

// SetRunning is kept for StatusProvider interface compatibility.
// Worker reports state via stream — no controller-side state to set.
func (m *StatusManager) SetRunning(gameserverID string) {
	// Worker reports state via stream — no controller-side state to set.
}

// ClearError removes any cached error reason for a gameserver.
// Called when starting a gameserver that was previously in error state so
// DeriveStatus doesn't return stale error during the new start sequence.
func (m *StatusManager) ClearError(gameserverID string) {
	m.workerStateMu.Lock()
	delete(m.errorReasons, gameserverID)
	delete(m.workerStates, gameserverID)
	m.workerStateMu.Unlock()
}

// ResetCrashCount clears the auto-restart crash counter for a gameserver.
// Called on user-initiated Start so they get a fresh retry budget.
func (m *StatusManager) ResetCrashCount(gameserverID string) {
	m.crashMu.Lock()
	delete(m.crashCounts, gameserverID)
	m.crashMu.Unlock()
}

// SetStopped clears the worker state cache so DeriveStatus doesn't show stale "running".
// Called by the lifecycle service after StopInstance completes.
func (m *StatusManager) SetStopped(gameserverID string) {
	m.workerStateMu.Lock()
	delete(m.workerStates, gameserverID)
	delete(m.errorReasons, gameserverID)
	m.workerStateMu.Unlock()
	m.stopPolling(gameserverID)
}

// InjectWorkerState sets the worker state for a gameserver. Used in tests to simulate
// the status subscriber having received a state update from the worker.
func (m *StatusManager) InjectWorkerState(gameserverID string, state *worker.InstanceStateUpdate) {
	m.workerStateMu.Lock()
	defer m.workerStateMu.Unlock()
	if state == nil {
		delete(m.workerStates, gameserverID)
	} else {
		m.workerStates[gameserverID] = state
	}
}

func (m *StatusManager) getWorkerState(gameserverID string) *worker.InstanceStateUpdate {
	m.workerStateMu.RLock()
	defer m.workerStateMu.RUnlock()
	return m.workerStates[gameserverID]
}

func (m *StatusManager) startPolling(gameserverID string) {
	if m.querySvc != nil {
		m.querySvc.StartPolling(gameserverID)
	}
	if m.statsPoller != nil {
		m.statsPoller.StartPolling(gameserverID)
	}
}

func (m *StatusManager) stopPolling(gameserverID string) {
	if m.querySvc != nil {
		m.querySvc.StopPolling(gameserverID)
	}
	if m.statsPoller != nil {
		m.statsPoller.StopPolling(gameserverID)
	}
}

// DeriveStatus computes the user-facing status for a gameserver by combining
// worker-reported state, operation phase, archive flag, and worker reachability.
// This is the single source of truth — no status column needed.
func (m *StatusManager) DeriveStatus(gs *model.Gameserver) (status string, errorReason string) {
	if gs.DesiredState == "archived" {
		return controller.StatusArchived, ""
	}

	// Check worker reachability
	if gs.NodeID != nil {
		if _, online := m.registry.Get(*gs.NodeID); !online {
			return controller.StatusUnreachable, "Worker offline"
		}
	}

	// Check for controller-side errors (migration failed, backup restore failed, etc.)
	m.workerStateMu.RLock()
	errReason := m.errorReasons[gs.ID]
	m.workerStateMu.RUnlock()
	if errReason != "" {
		return controller.StatusError, errReason
	}

	// Get worker-reported state
	ws := m.getWorkerState(gs.ID)

	// Check operation phase first — operations override worker state display
	if gs.Operation != nil {
		switch gs.Operation.Phase {
		case model.PhasePullingImage, model.PhaseDownloadingGame, model.PhaseInstalling:
			return controller.StatusInstalling, ""
		case model.PhaseStopping:
			return controller.StatusStopping, ""
		}
	}

	if ws == nil {
		// No worker state yet — if desired_state is "running", an async operation
		// is in progress (doStart hasn't created the instance yet)
		if gs.DesiredState == "running" {
			return controller.StatusStarting, ""
		}
		return controller.StatusStopped, ""
	}

	switch ws.State {
	case worker.StateCreated, worker.StateStarting:
		return controller.StatusStarting, ""
	case worker.StateRunning:
		return controller.StatusRunning, ""
	case worker.StateExited:
		if ws.ExitCode == 0 {
			return controller.StatusStopped, ""
		}
		return controller.StatusError, "Instance exited unexpectedly"
	}

	return controller.StatusStopped, ""
}

const maxAutoRestartAttempts = 3

// handleUnexpectedDeath handles an unexpected instance death. If auto-restart
// is enabled and the crash limit hasn't been reached, restarts the gameserver.
func (m *StatusManager) handleUnexpectedDeath(gs *model.Gameserver, reason string) {
	if gs.AutoRestart == nil || !*gs.AutoRestart || m.restartFunc == nil {
		m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: reason, Timestamp: time.Now()})
		return
	}

	m.crashMu.Lock()
	m.crashCounts[gs.ID]++
	count := m.crashCounts[gs.ID]
	m.crashMu.Unlock()

	if count > maxAutoRestartAttempts {
		m.log.Error("auto-restart limit reached, giving up", "gameserver", gs.ID, "attempts", maxAutoRestartAttempts, "reason", reason)
		gs.DesiredState = "stopped"
		m.store.UpdateGameserver(gs)
		m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Crashed %d times, auto-restart disabled. Last crash: %s", maxAutoRestartAttempts, reason), Timestamp: time.Now()})
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "gameserver", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)

	// Clear error state so DeriveStatus doesn't block on the previous crash
	m.workerStateMu.Lock()
	delete(m.errorReasons, gs.ID)
	delete(m.workerStates, gs.ID)
	m.workerStateMu.Unlock()

	go func() {
		if err := m.restartFunc(context.Background(), gs.ID); err != nil {
			m.log.Error("auto-restart failed", "gameserver", gs.ID, "attempt", count, "error", err)
			m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error()), Timestamp: time.Now()})
		}
	}()
}

// describeExit produces a human-readable crash reason from the exit code,
// uptime, and last-known resource usage.
func describeExit(exitCode int, uptime time.Duration, lastStats *controller.GameserverStatsEvent) string {
	uptimeStr := uptime.Round(time.Second).String()

	var reason string
	switch exitCode {
	case 137:
		reason = "Killed by system (out of memory)"
		if lastStats != nil && lastStats.MemoryLimitMB > 0 {
			pct := float64(lastStats.MemoryUsageMB) / float64(lastStats.MemoryLimitMB) * 100
			reason = fmt.Sprintf("Killed by system (out of memory — was using %d/%d MB, %.0f%%). Increase memory limit.", lastStats.MemoryUsageMB, lastStats.MemoryLimitMB, pct)
		}
	case 139:
		reason = "Crashed (segmentation fault)"
	case 143:
		reason = "Terminated by signal"
	case -1:
		reason = "Killed by signal"
	case 1:
		reason = "Server exited with error (exit code 1). Check console for details."
	case 2:
		reason = "Server exited (interrupted)"
	default:
		if exitCode > 128 {
			reason = fmt.Sprintf("Killed by signal %d", exitCode-128)
		} else {
			reason = fmt.Sprintf("Server exited with code %d. Check console for details.", exitCode)
		}
	}

	return fmt.Sprintf("%s (after %s)", reason, uptimeStr)
}

func truncID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
