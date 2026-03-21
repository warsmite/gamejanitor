package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
)

type StatusManager struct {
	db           *sql.DB
	localWorker  worker.Worker
	log          *slog.Logger
	broadcaster  *EventBroadcaster
	querySvc     *QueryService
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

func NewStatusManager(db *sql.DB, localWorker worker.Worker, broadcaster *EventBroadcaster, querySvc *QueryService, readyWatcher *ReadyWatcher, dispatcher *worker.Dispatcher, registry *worker.Registry, restartFunc func(ctx context.Context, id string) error, log *slog.Logger) *StatusManager {
	sm := &StatusManager{
		db:            db,
		localWorker:   localWorker,
		broadcaster:   broadcaster,
		querySvc:      querySvc,
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

// Start begins watching Docker events from the local worker.
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
				if ev.NewStatus == StatusRunning {
					m.crashMu.Lock()
					delete(m.crashCounts, ev.GameserverID)
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
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusStopped, "")
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
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusStarted, "")
		m.readyWatcher.Watch(gs.ID, w, *gs.ContainerID)
	case "exited", "dead", "created":
		m.log.Info("container is not running, setting stopped", "id", gs.ID, "state", info.State)
		m.clearContainerAndSetStatus(gs, StatusStopped)
	default:
		m.log.Warn("container in unexpected state, setting error", "id", gs.ID, "state", info.State)
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError, "Container found in unexpected state.")
	}
}

// clearContainerAndSetStatus clears the container_id and updates status in one DB write.
func (m *StatusManager) clearContainerAndSetStatus(gs *models.Gameserver, newStatus string) {
	oldStatus := gs.Status
	gs.ContainerID = nil
	gs.Status = newStatus
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(m.db, gs); err != nil {
		m.log.Error("failed to clear container and update status", "id", gs.ID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("gameserver status changed", "id", gs.ID, "from", oldStatus, "to", newStatus)

	if m.broadcaster != nil {
		m.broadcaster.Publish(StatusEvent{
			GameserverID: gs.ID,
			OldStatus:    oldStatus,
			NewStatus:    newStatus,
			Timestamp:    time.Now(),
		})
	}
}

// watchWorkerEvents starts a goroutine that watches Docker events from a worker.
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
	gsID := strings.TrimPrefix(event.ContainerName, "gamejanitor-")
	if gsID == event.ContainerName {
		return
	}
	// Skip temp containers (update/reinstall/backup/fileops)
	if strings.Contains(gsID, "-update-") || strings.Contains(gsID, "-reinstall-") || strings.Contains(gsID, "-backup-") || strings.Contains(gsID, "-fileops-") {
		return
	}

	gs, err := models.GetGameserver(m.db, gsID)
	if err != nil || gs == nil {
		m.log.Debug("docker event for unknown gameserver", "container_name", event.ContainerName, "action", event.Action)
		return
	}

	w := m.workerForGameserver(gs)

	switch event.Action {
	case "start":
		m.log.Debug("docker event: container started", "id", gsID)

	case "die", "stop":
		m.readyWatcher.Stop(gsID)
		m.querySvc.StopPolling(gsID)
		if gs.Status == StatusStopping || gs.Status == StatusUpdating || gs.Status == StatusReinstalling || gs.Status == StatusMigrating || gs.Status == StatusRestoring {
			m.log.Debug("docker event: expected container stop", "id", gsID, "status", gs.Status)
		} else if gs.Status == StatusRunning || gs.Status == StatusStarted {
			m.log.Warn("docker event: unexpected container death", "id", gsID, "status", gs.Status, "action", event.Action)
			m.handleUnexpectedDeath(gs)
		}

	case "kill":
		m.log.Debug("docker event: container killed", "id", gsID)
	}

	_ = w
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
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError, "Gameserver stopped unexpectedly.")
		return
	}

	m.crashMu.Lock()
	m.crashCounts[gs.ID]++
	count := m.crashCounts[gs.ID]
	m.crashMu.Unlock()

	if count > maxAutoRestartAttempts {
		m.log.Error("auto-restart limit reached, giving up", "id", gs.ID, "attempts", maxAutoRestartAttempts)
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError,
			fmt.Sprintf("Crashed %d times, auto-restart disabled. Check logs.", maxAutoRestartAttempts))
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "id", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)
	go func() {
		if err := m.restartFunc(context.Background(), gs.ID); err != nil {
			m.log.Error("auto-restart failed", "id", gs.ID, "attempt", count, "error", err)
			setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError,
				fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error()))
		}
	}()
}
