package operation

import (
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

// Tracker manages the transient in-flight operation state for gameservers.
// Operations are held in memory only — not persisted to DB.
// Phase changes publish events via the event bus.
// Progress updates notify per-gameserver watchers (dedicated stream, not the event bus).
type Tracker struct {
	mu         sync.RWMutex
	operations map[string]*model.Operation
	watchers   map[string]map[uint64]chan *model.Operation
	nextWatch  uint64
	bus        *controller.EventBus
	log        *slog.Logger
}

func NewTracker(bus *controller.EventBus, log *slog.Logger) *Tracker {
	return &Tracker{
		operations: make(map[string]*model.Operation),
		watchers:   make(map[string]map[uint64]chan *model.Operation),
		bus:        bus,
		log:        log.With("component", "operation_tracker"),
	}
}

// SetOperation sets the current operation and phase for a gameserver.
// Publishes a gameserver.operation event and notifies watchers.
func (t *Tracker) SetOperation(gameserverID, opType string, phase model.OperationPhase) {
	op := &model.Operation{Type: opType, Phase: phase}

	t.mu.Lock()
	t.operations[gameserverID] = op
	t.notifyWatchersLocked(gameserverID, op)
	t.mu.Unlock()

	t.bus.Publish(controller.NewSystemEvent(controller.EventGameserverOperation, gameserverID, &controller.OperationData{
		Operation: &model.Operation{Type: opType, Phase: phase},
	}))

	t.log.Debug("operation set",
		"gameserver_id", gameserverID,
		"type", opType,
		"phase", phase,
	)
}

// UpdateProgress updates the progress on the current operation.
// Notifies watchers only (not the event bus — progress is high-frequency).
func (t *Tracker) UpdateProgress(gameserverID string, progress model.OperationProgress) {
	t.mu.Lock()
	op, ok := t.operations[gameserverID]
	if ok {
		op.Progress = &progress
		t.notifyWatchersLocked(gameserverID, t.copyOpLocked(gameserverID))
	}
	t.mu.Unlock()
}

// ClearOperation removes the active operation for a gameserver.
// Publishes a gameserver.operation event with nil and notifies watchers.
func (t *Tracker) ClearOperation(gameserverID string) {
	t.mu.Lock()
	_, had := t.operations[gameserverID]
	delete(t.operations, gameserverID)
	t.notifyWatchersLocked(gameserverID, nil)
	t.mu.Unlock()

	if !had {
		return
	}

	t.bus.Publish(controller.NewSystemEvent(controller.EventGameserverOperation, gameserverID, &controller.OperationData{
		Operation: nil,
	}))

	t.log.Debug("operation cleared", "gameserver_id", gameserverID)
}

// GetOperation returns the current operation for a gameserver, or nil.
func (t *Tracker) GetOperation(gameserverID string) *model.Operation {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.copyOpLocked(gameserverID)
}

// Watch registers a watcher for a gameserver's operation state.
// Returns a channel that receives the current operation on every change (phase or progress).
// The channel is buffered(1) — if the consumer is slow, intermediate updates are dropped
// and only the latest state is delivered. Call unwatch to unregister.
func (t *Tracker) Watch(gameserverID string) (ch <-chan *model.Operation, unwatch func()) {
	c := make(chan *model.Operation, 1)

	t.mu.Lock()
	t.nextWatch++
	id := t.nextWatch
	if t.watchers[gameserverID] == nil {
		t.watchers[gameserverID] = make(map[uint64]chan *model.Operation)
	}
	t.watchers[gameserverID][id] = c
	t.mu.Unlock()

	return c, func() {
		t.mu.Lock()
		delete(t.watchers[gameserverID], id)
		if len(t.watchers[gameserverID]) == 0 {
			delete(t.watchers, gameserverID)
		}
		t.mu.Unlock()
	}
}

// notifyWatchersLocked sends the operation to all watchers for a gameserver.
// Must be called with t.mu held. Non-blocking — drops if consumer is behind.
func (t *Tracker) notifyWatchersLocked(gameserverID string, op *model.Operation) {
	for _, ch := range t.watchers[gameserverID] {
		select {
		case ch <- op:
		default:
			// Consumer behind — drain old value and send latest
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- op:
			default:
			}
		}
	}
}

func (t *Tracker) copyOpLocked(gameserverID string) *model.Operation {
	op, ok := t.operations[gameserverID]
	if !ok {
		return nil
	}
	cp := *op
	if op.Progress != nil {
		p := *op.Progress
		cp.Progress = &p
	}
	return &cp
}
