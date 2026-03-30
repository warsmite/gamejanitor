package gameserver

import (
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

// OperationTracker manages the transient in-flight operation state for gameservers.
// Operations are held in memory only — not persisted to DB.
// Phase changes publish events; progress updates do not (UI polls instead).
type OperationTracker struct {
	mu         sync.RWMutex
	operations map[string]*model.Operation
	bus        *controller.EventBus
	log        *slog.Logger
}

func NewOperationTracker(bus *controller.EventBus, log *slog.Logger) *OperationTracker {
	return &OperationTracker{
		operations: make(map[string]*model.Operation),
		bus:        bus,
		log:        log.With("component", "operation_tracker"),
	}
}

// SetOperation sets the current operation and phase for a gameserver.
// Publishes a gameserver.operation event on every phase change.
func (t *OperationTracker) SetOperation(gameserverID, opType string, phase model.OperationPhase) {
	t.mu.Lock()
	t.operations[gameserverID] = &model.Operation{
		Type:  opType,
		Phase: phase,
	}
	t.mu.Unlock()

	t.bus.Publish(controller.OperationEvent{
		GameserverID: gameserverID,
		Operation:    &model.Operation{Type: opType, Phase: phase},
		Timestamp:    time.Now(),
	})

	t.log.Debug("operation set",
		"gameserver_id", gameserverID,
		"type", opType,
		"phase", phase,
	)
}

// UpdateProgress updates the progress on the current operation.
// Does not publish an event — the UI polls for progress via the API.
func (t *OperationTracker) UpdateProgress(gameserverID string, progress model.OperationProgress) {
	t.mu.Lock()
	op, ok := t.operations[gameserverID]
	if ok {
		op.Progress = &progress
	}
	t.mu.Unlock()
}

// ClearOperation removes the active operation for a gameserver.
// Publishes a gameserver.operation event with nil operation.
func (t *OperationTracker) ClearOperation(gameserverID string) {
	t.mu.Lock()
	_, had := t.operations[gameserverID]
	delete(t.operations, gameserverID)
	t.mu.Unlock()

	if !had {
		return
	}

	t.bus.Publish(controller.OperationEvent{
		GameserverID: gameserverID,
		Operation:    nil,
		Timestamp:    time.Now(),
	})

	t.log.Debug("operation cleared", "gameserver_id", gameserverID)
}

// GetOperation returns the current operation for a gameserver, or nil.
func (t *OperationTracker) GetOperation(gameserverID string) *model.Operation {
	t.mu.RLock()
	defer t.mu.RUnlock()

	op, ok := t.operations[gameserverID]
	if !ok {
		return nil
	}

	// Return a copy so callers can't mutate the tracked state
	copy := *op
	if op.Progress != nil {
		p := *op.Progress
		copy.Progress = &p
	}
	return &copy
}
