package gameserver

import (
	"context"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/worker"
)

type StatusManager struct {
	store       Store
	log         *slog.Logger
	broadcaster *event.EventBus
	querySvc    *status.QueryService
	statsPoller *status.StatsPoller
	dispatcher  *orchestrator.Dispatcher
	registry    *orchestrator.Registry
	restartFunc func(ctx context.Context, id string) error
	runner      *Runner

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

func NewStatusManager(store Store, broadcaster *event.EventBus, querySvc *status.QueryService, statsPoller *status.StatsPoller, dispatcher *orchestrator.Dispatcher, registry *orchestrator.Registry, restartFunc func(ctx context.Context, id string) error, runner *Runner, log *slog.Logger) *StatusManager {
	sm := &StatusManager{
		store:         store,
		broadcaster:   broadcaster,
		querySvc:      querySvc,
		statsPoller:   statsPoller,
		dispatcher:    dispatcher,
		registry:      registry,
		restartFunc:   restartFunc,
		runner:        runner,
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
				e, ok := ev.(event.Event)
				if !ok {
					continue
				}
				if e.Type == event.EventGameserverError {
					if data, ok := e.Data.(*event.ErrorData); ok {
						m.log.Warn("gameserver error event", "gameserver", e.GameserverID, "reason", data.Reason)
						m.workerStateMu.Lock()
						m.errorReasons[e.GameserverID] = data.Reason
						m.workerStateMu.Unlock()
						m.stopPolling(e.GameserverID)
					}
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

func truncID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
