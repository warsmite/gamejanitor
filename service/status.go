package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/naming"
	"github.com/warsmite/gamejanitor/worker"
)

type StatusManager struct {
	db           *sql.DB
	localWorker  worker.Worker
	log          *slog.Logger
	broadcaster  *EventBus
	querySvc     *QueryService
	statsPoller  *StatsPoller
	readyWatcher *ReadyWatcher
	dispatcher   *worker.Dispatcher
	registry     *worker.Registry
	restartFunc  func(ctx context.Context, id string) error

	cancel context.CancelFunc

	// Per-worker event watchers for multi-node
	workerCancels map[string]context.CancelFunc
	workerMu      sync.Mutex

	// Auto-restart crash counter: reset when gameserver reaches "running"
	crashCounts map[string]int
	crashMu     sync.Mutex
}

func NewStatusManager(db *sql.DB, localWorker worker.Worker, broadcaster *EventBus, querySvc *QueryService, statsPoller *StatsPoller, readyWatcher *ReadyWatcher, dispatcher *worker.Dispatcher, registry *worker.Registry, restartFunc func(ctx context.Context, id string) error, log *slog.Logger) *StatusManager {
	sm := &StatusManager{
		db:            db,
		localWorker:   localWorker,
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

	// Subscribe to worker registration events for multi-node event watching
	if registry != nil {
		registry.SetCallbacks(sm.onWorkerRegistered, sm.onWorkerUnregistered)
	}

	return sm
}

// Start begins watching container events from the local worker.
func (m *StatusManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	if m.localWorker != nil {
		m.watchWorkerEvents(ctx, "local", m.localWorker)
	}

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
				statusEv, isStatus := ev.(StatusEvent)
				if !isStatus {
					continue
				}
				if statusEv.NewStatus == StatusRunning {
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

	gameservers, err := models.ListGameservers(m.db, models.GameserverFilter{})
	if err != nil {
		return err
	}

	for _, gs := range gameservers {
		if !needsRecovery(gs.Status) {
			continue
		}

		// For multi-node: skip gameservers on remote workers (recovered when worker registers)
		w := m.workerForGameserver(&gs)
		if w == nil {
			m.log.Debug("skipping recovery for gameserver on offline worker", "id", gs.ID, "node_id", gs.NodeID)
			continue
		}

		m.recoverGameserver(ctx, &gs, w)
	}

	return nil
}

// workerForGameserver returns the appropriate worker, or nil if unavailable.
func (m *StatusManager) workerForGameserver(gs *models.Gameserver) worker.Worker {
	if m.dispatcher != nil {
		w := m.dispatcher.WorkerFor(gs.ID)
		return w
	}
	return m.localWorker
}

func (m *StatusManager) recoverGameserver(ctx context.Context, gs *models.Gameserver, w worker.Worker) {
	if gs.ContainerID == nil {
		m.log.Info("gameserver has no container, setting stopped", "id", gs.ID, "was_status", gs.Status)
		m.setRecoveryStatus(gs.ID, StatusStopped, "")
		return
	}

	info, err := w.InspectContainer(ctx, *gs.ContainerID)
	if err != nil {
		m.log.Warn("container not found, setting stopped", "id", gs.ID, "container_id", (*gs.ContainerID)[:12], "error", err)
		m.clearContainerAndSetStatus(gs, StatusStopped)
		return
	}

	switch info.State {
	case "running":
		m.log.Info("container running, re-attaching ready watcher", "id", gs.ID)
		m.setRecoveryStatus(gs.ID, StatusStarted, "")
		m.readyWatcher.Watch(gs.ID, w, *gs.ContainerID)
	case "exited", "dead", "created":
		m.log.Info("container is not running, setting stopped", "id", gs.ID, "state", info.State)
		m.clearContainerAndSetStatus(gs, StatusStopped)
	default:
		m.log.Warn("container in unexpected state, setting error", "id", gs.ID, "state", info.State)
		m.setRecoveryStatus(gs.ID, StatusError, "Container found in unexpected state.")
	}
}

// setRecoveryStatus directly writes status to DB without publishing events.
// Used during startup recovery to reconcile DB with Docker reality.
func (m *StatusManager) setRecoveryStatus(id string, newStatus string, errorReason string) {
	gs, err := models.GetGameserver(m.db, id)
	if err != nil || gs == nil {
		m.log.Error("recovery: failed to get gameserver", "id", id, "error", err)
		return
	}
	oldStatus := gs.Status
	gs.Status = newStatus
	if newStatus == StatusError {
		gs.ErrorReason = errorReason
	} else {
		gs.ErrorReason = ""
	}
	if err := models.UpdateGameserver(m.db, gs); err != nil {
		m.log.Error("recovery: failed to update status", "id", id, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("recovery: status set", "id", id, "from", oldStatus, "to", newStatus)
}

// clearContainerAndSetStatus clears the container_id and updates status in one DB write.
// Used during startup recovery — no events published.
func (m *StatusManager) clearContainerAndSetStatus(gs *models.Gameserver, newStatus string) {
	oldStatus := gs.Status
	gs.ContainerID = nil
	gs.Status = newStatus
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(m.db, gs); err != nil {
		m.log.Error("recovery: failed to clear container and update status", "id", gs.ID, "from", oldStatus, "to", newStatus, "error", err)
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

	gs, err := models.GetGameserver(m.db, gsID)
	if err != nil || gs == nil {
		m.log.Debug("container event for unknown gameserver", "container_name", event.ContainerName, "action", event.Action)
		return
	}

	switch event.Action {
	case "start":
		m.log.Debug("container event: container started", "id", gsID)

	case "die", "stop":
		m.readyWatcher.Stop(gsID)
		m.querySvc.StopPolling(gsID)
		m.statsPoller.StopPolling(gsID)
		if gs.Status == StatusStopping {
			m.log.Debug("container event: expected container stop", "id", gsID, "status", gs.Status)
		} else if gs.Status == StatusRunning || gs.Status == StatusStarted || gs.Status == StatusInstalling || gs.Status == StatusStarting {
			m.log.Warn("container event: unexpected container death", "id", gsID, "status", gs.Status, "action", event.Action)
			m.broadcaster.Publish(ContainerExitedEvent{GameserverID: gsID, Timestamp: time.Now()})
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

	m.broadcaster.Publish(WorkerEvent{
		Type:      EventWorkerConnected,
		Timestamp: time.Now(),
		Actor:     SystemActor,
		WorkerID:  nodeID,
	})

	// Recover gameservers on this worker
	go m.recoverWorkerGameservers(ctx, nodeID, w)
}

// onWorkerUnregistered is called when a remote worker is unregistered (timeout or explicit).
func (m *StatusManager) onWorkerUnregistered(nodeID string) {
	// Log impact before tearing down
	gameservers, err := models.ListGameservers(m.db, models.GameserverFilter{NodeID: &nodeID})
	if err != nil {
		m.log.Error("failed to query gameservers for disconnected worker", "worker_id", nodeID, "error", err)
	} else if len(gameservers) > 0 {
		m.log.Warn("worker disconnected, gameservers on node are now unreachable",
			"worker_id", nodeID, "affected_gameservers", len(gameservers))
	}

	m.workerMu.Lock()
	if cancel, ok := m.workerCancels[nodeID]; ok {
		cancel()
		delete(m.workerCancels, nodeID)
	}
	m.workerMu.Unlock()

	m.broadcaster.Publish(WorkerEvent{
		Type:      EventWorkerDisconnected,
		Timestamp: time.Now(),
		Actor:     SystemActor,
		WorkerID:  nodeID,
	})

	m.log.Info("stopped event watcher for disconnected worker", "worker_id", nodeID)
}

// recoverWorkerGameservers recovers gameservers assigned to a specific worker node.
func (m *StatusManager) recoverWorkerGameservers(ctx context.Context, nodeID string, w worker.Worker) {
	gameservers, err := models.ListGameservers(m.db, models.GameserverFilter{})
	if err != nil {
		m.log.Error("failed to list gameservers for worker recovery", "worker_id", nodeID, "error", err)
		return
	}

	for _, gs := range gameservers {
		if gs.NodeID == nil || *gs.NodeID != nodeID {
			continue
		}
		if !needsRecovery(gs.Status) {
			continue
		}
		m.log.Info("recovering gameserver on reconnected worker", "id", gs.ID, "worker_id", nodeID)
		m.recoverGameserver(ctx, &gs, w)
	}
}

const maxAutoRestartAttempts = 3

// handleUnexpectedDeath handles an unexpected container death. If auto-restart
// is enabled and the crash limit hasn't been reached, restarts the gameserver.
func (m *StatusManager) handleUnexpectedDeath(gs *models.Gameserver) {
	if !gs.AutoRestart || m.restartFunc == nil {
		m.broadcaster.Publish(GameserverErrorEvent{GameserverID: gs.ID, Reason: "Gameserver stopped unexpectedly.", Timestamp: time.Now()})
		return
	}

	m.crashMu.Lock()
	m.crashCounts[gs.ID]++
	count := m.crashCounts[gs.ID]
	m.crashMu.Unlock()

	if count > maxAutoRestartAttempts {
		m.log.Error("auto-restart limit reached, giving up", "id", gs.ID, "attempts", maxAutoRestartAttempts)
		m.broadcaster.Publish(GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Crashed %d times, auto-restart disabled. Check logs.", maxAutoRestartAttempts), Timestamp: time.Now()})
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "id", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)
	go func() {
		if err := m.restartFunc(context.Background(), gs.ID); err != nil {
			m.log.Error("auto-restart failed", "id", gs.ID, "attempt", count, "error", err)
			m.broadcaster.Publish(GameserverErrorEvent{GameserverID: gs.ID, Reason: fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error()), Timestamp: time.Now()})
		}
	}()
}
