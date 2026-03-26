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
	readyWatcher *ReadyWatcher
	dispatcher   *orchestrator.Dispatcher
	registry     *orchestrator.Registry
	restartFunc  func(ctx context.Context, id string) error

	cancel context.CancelFunc

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

	// Reset crash counter when a gameserver successfully reaches "running"
	events, unsub := m.broadcaster.Subscribe()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-events:
				if !ok {
					return
				}
				statusEv, isStatus := ev.(controller.StatusEvent)
				if !isStatus {
					continue
				}
				if statusEv.NewStatus == controller.StatusRunning {
					m.crashMu.Lock()
					delete(m.crashCounts, statusEv.GameserverID)
					m.crashMu.Unlock()
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

	m.log.Info("status manager stopped")
}

// RecoverOnStartup reconciles DB status with Docker reality.
// Any gameserver not in a terminal state (stopped/error) is checked against
// the actual Docker container and corrected.
func (m *StatusManager) RecoverOnStartup(ctx context.Context) error {
	m.log.Info("recovering gameserver status from docker state")

	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		return err
	}

	var withContainer, containerMissing int

	for _, gs := range gameservers {
		if !controller.NeedsRecovery(gs.Status) {
			continue
		}

		w := m.workerForGameserver(&gs)
		if w == nil {
			// Worker is offline — mark gameserver unreachable instead of leaving stale status
			if gs.NodeID != nil {
				m.log.Warn("marking gameserver unreachable, worker offline", "id", gs.ID, "node_id", *gs.NodeID)
				m.setRecoveryStatus(gs.ID, controller.StatusUnreachable, "Worker offline at startup.")
			}
			continue
		}

		if gs.ContainerID != nil {
			withContainer++
		}
		if m.recoverGameserver(ctx, &gs, w) {
			containerMissing++
		}
	}

	if withContainer > 0 && containerMissing == withContainer {
		m.log.Warn("all gameserver containers are missing — did you switch container runtimes (Docker ↔ Podman)? Volumes may need manual migration",
			"expected_containers", withContainer,
		)
	}

	return nil
}

// workerForGameserver returns the appropriate worker, or nil if unavailable.
func (m *StatusManager) workerForGameserver(gs *model.Gameserver) worker.Worker {
	return m.dispatcher.WorkerFor(gs.ID)
}

// recoverGameserver reconciles a single gameserver's DB status with container reality.
// Returns true if the gameserver had a container ID but the container was not found.
func (m *StatusManager) recoverGameserver(ctx context.Context, gs *model.Gameserver, w worker.Worker) bool {
	if gs.ContainerID == nil {
		m.log.Info("gameserver has no container, setting stopped", "id", gs.ID, "was_status", gs.Status)
		m.setRecoveryStatus(gs.ID, controller.StatusStopped, "")
		return false
	}

	info, err := w.InspectContainer(ctx, *gs.ContainerID)
	if err != nil {
		m.log.Warn("container not found, setting stopped", "id", gs.ID, "container_id", (*gs.ContainerID)[:12], "error", err)
		m.clearContainerAndSetStatus(gs, controller.StatusStopped)
		return true
	}

	switch info.State {
	case "running":
		m.log.Info("container running, re-attaching ready watcher", "id", gs.ID)
		m.setRecoveryStatus(gs.ID, controller.StatusStarted, "")
		m.readyWatcher.Watch(gs.ID, w, *gs.ContainerID)
	case "exited", "dead", "created":
		m.log.Info("container is not running, setting stopped", "id", gs.ID, "state", info.State)
		m.clearContainerAndSetStatus(gs, controller.StatusStopped)
	default:
		m.log.Warn("container in unexpected state, setting error", "id", gs.ID, "state", info.State)
		m.setRecoveryStatus(gs.ID, controller.StatusError, "Container found in unexpected state.")
	}
	return false
}

// setRecoveryStatus records a status_changed activity without publishing events.
// Used during startup recovery to reconcile DB with Docker reality.
func (m *StatusManager) setRecoveryStatus(id string, newStatus string, errorReason string) {
	gs, err := m.store.GetGameserver(id)
	if err != nil || gs == nil {
		m.log.Error("recovery: failed to get gameserver", "id", id, "error", err)
		return
	}
	oldStatus := gs.Status
	if newStatus != controller.StatusError {
		errorReason = ""
	}
	if err := recordStatusActivity(m.store, id, newStatus, errorReason); err != nil {
		m.log.Error("recovery: failed to record status_changed activity", "id", id, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("recovery: status set", "id", id, "from", oldStatus, "to", newStatus)
}

// clearContainerAndSetStatus clears the container_id and records a status_changed activity.
// Used during startup recovery — no events published.
func (m *StatusManager) clearContainerAndSetStatus(gs *model.Gameserver, newStatus string) {
	oldStatus := gs.Status
	gs.ContainerID = nil
	if err := m.store.UpdateGameserver(gs); err != nil {
		m.log.Error("recovery: failed to clear container", "id", gs.ID, "error", err)
		return
	}
	if err := recordStatusActivity(m.store, gs.ID, newStatus, ""); err != nil {
		m.log.Error("recovery: failed to record status_changed activity", "id", gs.ID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("recovery: status set", "id", gs.ID, "from", oldStatus, "to", newStatus)
}

// watchWorkerEvents starts a goroutine that watches container events from a worker.
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

func (m *StatusManager) handleEvent(event worker.ContainerEvent) {
	gsID, ok := naming.GameserverIDFromContainerName(event.ContainerName)
	if !ok {
		return
	}

	gs, err := m.store.GetGameserver(gsID)
	if err != nil || gs == nil {
		m.log.Debug("container event for unknown gameserver", "container_name", event.ContainerName, "action", event.Action)
		return
	}

	switch event.Action {
	case "start":
		m.log.Debug("container event: container started", "id", gsID)

	case "die", "stop":
		// Ignore stale events from old containers (e.g. previous container's "die"
		// arriving after a new start has begun)
		if gs.ContainerID != nil && *gs.ContainerID != event.ContainerID {
			m.log.Debug("container event: ignoring stale event from old container", "id", gsID, "event_container", event.ContainerID[:12], "current_container", (*gs.ContainerID)[:12])
			return
		}
		// If ContainerID was cleared (restart in progress), this is a stale event
		if gs.ContainerID == nil && gs.Status != controller.StatusStopping {
			m.log.Debug("container event: ignoring event with no current container", "id", gsID, "status", gs.Status, "action", event.Action)
			return
		}

		m.readyWatcher.Stop(gsID)
		m.querySvc.StopPolling(gsID)
		m.statsPoller.StopPolling(gsID)
		if gs.Status == controller.StatusStopping {
			m.log.Debug("container event: expected container stop", "id", gsID, "status", gs.Status)
		} else if gs.Status == controller.StatusRunning || gs.Status == controller.StatusStarted || gs.Status == controller.StatusInstalling || gs.Status == controller.StatusStarting {
			m.log.Warn("container event: unexpected container death", "id", gsID, "status", gs.Status, "action", event.Action)
			m.broadcaster.Publish(controller.ContainerExitedEvent{GameserverID: gsID, Timestamp: time.Now()})
			m.handleUnexpectedDeath(gs)
		}

	case "kill":
		m.log.Debug("container event: container killed", "id", gsID)
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

	m.log.Info("starting event watcher for remote worker", "worker_id", nodeID)
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
	gameservers, err := m.store.ListGameservers(model.GameserverFilter{NodeID: &nodeID})
	if err != nil {
		m.log.Error("failed to query gameservers for disconnected worker", "worker_id", nodeID, "error", err)
	} else {
		for _, gs := range gameservers {
			if controller.NeedsRecovery(gs.Status) {
				m.log.Warn("marking gameserver unreachable due to worker disconnect",
					"gameserver_id", gs.ID, "worker_id", nodeID, "was_status", gs.Status)
				m.setRecoveryStatus(gs.ID, controller.StatusUnreachable, "Worker disconnected.")
			}
		}
	}

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

	m.log.Info("stopped event watcher for disconnected worker", "worker_id", nodeID)
}

// recoverWorkerGameservers recovers gameservers assigned to a specific worker node
// and detects orphan containers (running on Docker but not tracked in DB).
func (m *StatusManager) recoverWorkerGameservers(ctx context.Context, nodeID string, w worker.Worker) {
	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		m.log.Error("failed to list gameservers for worker recovery", "worker_id", nodeID, "error", err)
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
		m.log.Info("recovering gameserver on reconnected worker", "id", gs.ID, "worker_id", nodeID, "was_status", gs.Status)
		m.recoverGameserver(ctx, &gs, w)
	}

	// Reverse check: Docker → DB (orphan detection)
	m.detectOrphanContainers(ctx, nodeID, w, knownIDs)
}

// detectOrphanContainers finds gamejanitor containers running on a worker that
// aren't tracked in the database. These are logged as warnings — not auto-removed,
// as they may contain player data (e.g. after a DB restore).
func (m *StatusManager) detectOrphanContainers(ctx context.Context, nodeID string, w worker.Worker, knownIDs map[string]bool) {
	containers, err := w.ListGameserverContainers(ctx)
	if err != nil {
		m.log.Warn("failed to list containers for orphan detection", "worker_id", nodeID, "error", err)
		return
	}

	for _, c := range containers {
		if knownIDs[c.GameserverID] {
			continue
		}
		// Also check gameservers on other nodes (might have been migrated)
		gs, _ := m.store.GetGameserver(c.GameserverID)
		if gs != nil {
			continue
		}
		m.log.Warn("orphan container detected — container exists on worker but gameserver not found in database",
			"worker_id", nodeID, "container_id", c.ContainerID[:12], "container_name", c.ContainerName,
			"gameserver_id", c.GameserverID, "state", c.State)
	}
}

const maxAutoRestartAttempts = 3

// handleUnexpectedDeath handles an unexpected container death. If auto-restart
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
		m.log.Error("auto-restart limit reached, giving up", "id", gs.ID, "attempts", maxAutoRestartAttempts)
		m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Crashed %d times, auto-restart disabled. Check logs.", maxAutoRestartAttempts), Timestamp: time.Now()})
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "id", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)
	go func() {
		if err := m.restartFunc(context.Background(), gs.ID); err != nil {
			m.log.Error("auto-restart failed", "id", gs.ID, "attempt", count, "error", err)
			m.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error()), Timestamp: time.Now()})
		}
	}()
}
