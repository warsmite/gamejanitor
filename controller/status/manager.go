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

// InstanceState tracks the runtime state of a gameserver's instance.
type InstanceState struct {
	Running     bool
	Exited      bool
	ExitCode    int
	ErrorReason string
	Ready       bool
}

type StatusManager struct {
	store       Store
	log         *slog.Logger
	broadcaster *controller.EventBus
	querySvc    *QueryService
	statsPoller *StatsPoller
	readyWatcher *ReadyWatcher
	dispatcher   *orchestrator.Dispatcher
	registry     *orchestrator.Registry
	restartFunc  func(ctx context.Context, id string) error

	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Runtime state: the source of truth for instance lifecycle
	runtimeMu     sync.RWMutex
	runtimeStates map[string]*InstanceState // gameserverID → state

	// Per-worker event watchers for multi-node
	workerCancels map[string]context.CancelFunc
	workerMu      sync.Mutex

	// Auto-restart crash counter: reset when gameserver reaches "running"
	crashCounts map[string]int
	crashMu     sync.Mutex
}

func NewStatusManager(store Store, broadcaster *controller.EventBus, querySvc *QueryService, statsPoller *StatsPoller, readyWatcher *ReadyWatcher, dispatcher *orchestrator.Dispatcher, registry *orchestrator.Registry, restartFunc func(ctx context.Context, id string) error, log *slog.Logger) *StatusManager {
	sm := &StatusManager{
		store:         store,
		broadcaster:   broadcaster,
		querySvc:      querySvc,
		statsPoller:   statsPoller,
		readyWatcher:  readyWatcher,
		dispatcher:    dispatcher,
		registry:      registry,
		restartFunc:   restartFunc,
		log:           log,
		runtimeStates: make(map[string]*InstanceState),
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

	// Listen for lifecycle events to update runtime state
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
				case controller.GameserverReadyEvent:
					m.setReady(e.GameserverID)
					m.crashMu.Lock()
					delete(m.crashCounts, e.GameserverID)
					m.crashMu.Unlock()
				case controller.GameserverErrorEvent:
					m.setError(e.GameserverID, e.Reason)
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

// RecoverOnStartup reconciles DB status with Docker reality.
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
		m.SetStopped(gs.ID)
		return false
	}

	info, err := w.InspectInstance(ctx, *gs.InstanceID)
	if err != nil {
		m.log.Warn("instance not found, clearing", "gameserver", gs.ID, "instance_id", (*gs.InstanceID)[:12], "error", err)
		gs.InstanceID = nil
		m.store.UpdateGameserver(gs)
		m.SetStopped(gs.ID)
		return true
	}

	switch info.State {
	case "running":
		m.log.Info("instance running, re-attaching ready watcher", "gameserver", gs.ID)
		m.SetRunning(gs.ID)
		m.readyWatcher.Watch(gs.ID, w, *gs.InstanceID)
	case "exited", "dead", "created":
		m.log.Info("instance is not running, clearing", "gameserver", gs.ID, "state", info.State)
		gs.InstanceID = nil
		m.store.UpdateGameserver(gs)
		m.SetStopped(gs.ID)
	default:
		m.log.Warn("instance in unexpected state", "gameserver", gs.ID, "state", info.State)
		m.setExited(gs.ID, -1, "Instance found in unexpected state.")
	}
	return false
}

// watchWorkerEvents starts a goroutine that watches instance events from a worker.
func (m *StatusManager) watchWorkerEvents(ctx context.Context, label string, w worker.Worker) {
	eventCh, errCh := w.WatchEvents(ctx)

	go func() {
		m.log.Debug("watching events", "worker", label)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errCh:
				if !ok {
					return
				}
				m.log.Error("event watcher error", "worker", label, "error", err)
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}
				m.handleEvent(event)
			}
		}
	}()
}

func (m *StatusManager) handleEvent(event worker.InstanceEvent) {
	gsID, ok := naming.GameserverIDFromInstanceName(event.InstanceName)
	if !ok {
		return
	}

	gs, err := m.store.GetGameserver(gsID)
	if err != nil || gs == nil {
		m.log.Debug("instance event for unknown gameserver", "instance_name", event.InstanceName, "action", event.Action)
		return
	}

	switch event.Action {
	case "start":
		m.log.Debug("instance event: instance started", "gameserver", gsID)

	case "die", "stop":
		// Ignore stale events from old instances
		if gs.InstanceID != nil && *gs.InstanceID != event.InstanceID {
			m.log.Debug("instance event: ignoring stale event from old instance", "gameserver", gsID, "event_instance", event.InstanceID[:12], "current_instance", (*gs.InstanceID)[:12])
			return
		}
		if gs.InstanceID == nil {
			m.log.Debug("instance event: ignoring event with no current instance", "gameserver", gsID, "action", event.Action)
			return
		}

		m.readyWatcher.Stop(gsID)
		m.querySvc.StopPolling(gsID)
		m.statsPoller.StopPolling(gsID)

		// Check if this was an expected stop (lifecycle already cleared the state)
		// or an unexpected death (instance crashed while running)
		rs := m.GetRuntimeState(gsID)
		if rs != nil && rs.Running {
			// Instance was still marked as running — this is unexpected
			m.log.Warn("instance event: unexpected instance death", "gameserver", gsID, "action", event.Action)
			m.setExited(gsID, -1, "Instance exited unexpectedly")
			m.broadcaster.Publish(controller.InstanceExitedEvent{GameserverID: gsID, Timestamp: time.Now()})
			m.handleUnexpectedDeath(gs)
		} else {
			m.log.Debug("instance event: expected instance stop", "gameserver", gsID)
		}

	case "kill":
		m.log.Debug("instance event: instance killed", "gameserver", gsID)
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
// and detects orphan instances (running on Docker but not tracked in DB).
func (m *StatusManager) recoverWorkerGameservers(ctx context.Context, nodeID string, w worker.Worker) {
	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		m.log.Error("failed to list gameservers for worker recovery", "worker", nodeID, "error", err)
		return
	}

	// Forward check: DB → Docker (existing recovery)
	knownIDs := make(map[string]bool)
	for _, gs := range gameservers {
		if gs.NodeID == nil || *gs.NodeID != nodeID {
			continue
		}
		knownIDs[gs.ID] = true
		if !controller.NeedsRecoveryOnReconnect(gs.Status) {
			continue
		}
		m.log.Info("recovering gameserver on reconnected worker", "gameserver", gs.ID, "worker", nodeID, "was_status", gs.Status)
		m.recoverGameserver(ctx, &gs, w)
	}

	// Reverse check: Docker → DB (orphan detection)
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
			"worker", nodeID, "instance_id", c.InstanceID[:12], "instance_name", c.InstanceName,
			"gameserver", c.GameserverID, "state", c.State)
	}
}

// --- Runtime state methods ---

// SetRunning marks a gameserver's instance as running (not yet ready).
// Called by the lifecycle service after StartInstance succeeds.
func (m *StatusManager) SetRunning(gameserverID string) {
	m.runtimeMu.Lock()
	m.runtimeStates[gameserverID] = &InstanceState{Running: true}
	m.runtimeMu.Unlock()
}

// SetStopped clears a gameserver's runtime state.
// Called by the lifecycle service after StopInstance completes.
func (m *StatusManager) SetStopped(gameserverID string) {
	m.runtimeMu.Lock()
	delete(m.runtimeStates, gameserverID)
	m.runtimeMu.Unlock()
}

func (m *StatusManager) setReady(gameserverID string) {
	m.runtimeMu.Lock()
	if s, ok := m.runtimeStates[gameserverID]; ok {
		s.Ready = true
	}
	m.runtimeMu.Unlock()
}

func (m *StatusManager) setExited(gameserverID string, exitCode int, reason string) {
	m.runtimeMu.Lock()
	m.runtimeStates[gameserverID] = &InstanceState{Exited: true, ExitCode: exitCode, ErrorReason: reason}
	m.runtimeMu.Unlock()
}

func (m *StatusManager) setError(gameserverID string, reason string) {
	m.runtimeMu.Lock()
	m.runtimeStates[gameserverID] = &InstanceState{Exited: true, ErrorReason: reason}
	m.runtimeMu.Unlock()
}

// GetRuntimeState returns the current instance state for a gameserver.
func (m *StatusManager) GetRuntimeState(gameserverID string) *InstanceState {
	m.runtimeMu.RLock()
	defer m.runtimeMu.RUnlock()
	return m.runtimeStates[gameserverID]
}

// DeriveStatus computes the user-facing status for a gameserver by combining
// runtime state, operation phase, archive flag, and worker reachability.
// This is the single source of truth — no status column needed.
func (m *StatusManager) DeriveStatus(gs *model.Gameserver) (status string, errorReason string) {
	if gs.Archived {
		return controller.StatusArchived, ""
	}

	// Check worker reachability
	if gs.NodeID != nil {
		if _, online := m.registry.Get(*gs.NodeID); !online {
			return controller.StatusUnreachable, "Worker offline"
		}
	}

	// Check runtime instance state first — this is ground truth
	rs := m.GetRuntimeState(gs.ID)

	if rs != nil && rs.Exited {
		reason := rs.ErrorReason
		if reason == "" {
			reason = "Instance exited unexpectedly"
		}
		return controller.StatusError, reason
	}

	if rs != nil && rs.Running {
		// Instance is running — check if ready or still starting
		if rs.Ready {
			return controller.StatusRunning, ""
		}
		// Check operation phase for more specific status
		if gs.Operation != nil && gs.Operation.Phase == model.PhaseStarting {
			return controller.StatusStarting, ""
		}
		return controller.StatusStarted, ""
	}

	// No running/exited instance. If a lifecycle operation is in progress and
	// hasn't created an instance yet, show the operation phase. Otherwise stopped.
	if gs.Operation != nil && gs.InstanceID == nil {
		switch gs.Operation.Phase {
		case model.PhasePullingImage, model.PhaseDownloadingGame, model.PhaseInstalling:
			return controller.StatusInstalling, ""
		case model.PhaseStopping:
			return controller.StatusStopping, ""
		}
	}

	return controller.StatusStopped, ""
}

const maxAutoRestartAttempts = 3

// handleUnexpectedDeath handles an unexpected instance death. If auto-restart
// is enabled and the crash limit hasn't been reached, restarts the gameserver.
func (m *StatusManager) handleUnexpectedDeath(gs *model.Gameserver) {
	if gs.AutoRestart == nil || !*gs.AutoRestart || m.restartFunc == nil {
		m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: "Gameserver stopped unexpectedly.", Timestamp: time.Now()})
		return
	}

	m.crashMu.Lock()
	m.crashCounts[gs.ID]++
	count := m.crashCounts[gs.ID]
	m.crashMu.Unlock()

	if count > maxAutoRestartAttempts {
		m.log.Error("auto-restart limit reached, giving up", "gameserver", gs.ID, "attempts", maxAutoRestartAttempts)
		m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Crashed %d times, auto-restart disabled. Check logs.", maxAutoRestartAttempts), Timestamp: time.Now()})
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "gameserver", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)
	go func() {
		if err := m.restartFunc(context.Background(), gs.ID); err != nil {
			m.log.Error("auto-restart failed", "gameserver", gs.ID, "attempt", count, "error", err)
			m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error()), Timestamp: time.Now()})
		}
	}()
}
