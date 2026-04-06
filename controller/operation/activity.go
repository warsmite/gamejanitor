package operation

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

type activityContextKey struct{}

// WithActivityID attaches an operation/event ID to the context.
func WithActivityID(ctx context.Context, activityID string) context.Context {
	return context.WithValue(ctx, activityContextKey{}, activityID)
}

// ActivityIDFromContext returns the operation ID from context, if any.
func ActivityIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(activityContextKey{}).(string); ok {
		return v
	}
	return ""
}

// EventRecorder abstracts event persistence and gameserver status updates.
type EventRecorder interface {
	CreateEvent(e *model.Event) error
	GetGameserver(id string) (*model.Gameserver, error)
	UpdateGameserver(gs *model.Gameserver) error
}

// ActivityTracker manages operation lifecycle via events and gameserver status.
type ActivityTracker struct {
	store EventRecorder
	log   *slog.Logger
}

func NewActivityTracker(store EventRecorder, log *slog.Logger) *ActivityTracker {
	return &ActivityTracker{store: store, log: log}
}

// Start records a new operation. Sets gameservers.operation to block concurrent ops.
// Returns the event ID. Stop and delete are always allowed (no mutex check).
func (t *ActivityTracker) Start(gameserverID, workerID, opType string, actor json.RawMessage, data json.RawMessage) (string, error) {
	gs, err := t.store.GetGameserver(gameserverID)
	if err != nil {
		return "", err
	}
	if gs == nil {
		return "", fmt.Errorf("gameserver %s not found", gameserverID)
	}

	if opType != model.OpStop && opType != model.OpDelete && gs.OperationType != nil {
		return "", fmt.Errorf("gameserver %s already has an operation in progress (%s)", gameserverID, *gs.OperationType)
	}

	eventID := uuid.New().String()
	if err := t.recordEvent(eventID, &gameserverID, workerID, controller.EventTypeForOp(opType), actor, data); err != nil {
		return "", err
	}

	gs.OperationType = &opType
	gs.OperationID = &eventID
	if err := t.store.UpdateGameserver(gs); err != nil {
		t.log.Error("failed to set operation on gameserver", "gameserver", gameserverID, "error", err)
	}

	t.log.Info("operation started", "event", eventID, "type", opType, "gameserver", gameserverID)
	return eventID, nil
}

// Complete clears the active operation on the gameserver.
func (t *ActivityTracker) Complete(gameserverID string) {
	if gameserverID == "" {
		return
	}
	t.clearOperation(gameserverID)
}

// Fail logs the error and clears the active operation on the gameserver.
func (t *ActivityTracker) Fail(gameserverID string, reason error) {
	if gameserverID == "" {
		return
	}
	if reason != nil {
		t.log.Warn("operation failed", "gameserver", gameserverID, "error", reason)
	}
	t.clearOperation(gameserverID)
}

func (t *ActivityTracker) clearOperation(gameserverID string) {
	gs, err := t.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		return
	}
	gs.OperationType = nil
	gs.OperationID = nil
	if err := t.store.UpdateGameserver(gs); err != nil {
		t.log.Error("failed to clear operation", "gameserver", gameserverID, "error", err)
	}
}

// RecordInstant creates an event for operations that complete immediately (CRUD, etc.).
func (t *ActivityTracker) RecordInstant(gameserverID *string, eventType string, actor json.RawMessage, data json.RawMessage) error {
	return t.recordEvent(uuid.New().String(), gameserverID, "", eventType, actor, data)
}

func (t *ActivityTracker) recordEvent(id string, gameserverID *string, workerID, eventType string, actor json.RawMessage, data json.RawMessage) error {
	if actor == nil {
		actor = json.RawMessage(`{}`)
	}
	if data == nil {
		data = json.RawMessage(`{}`)
	}

	e := &model.Event{
		ID:           id,
		GameserverID: gameserverID,
		WorkerID:     workerID,
		Type:         eventType,
		Actor:        actor,
		Data:         data,
		CreatedAt:    time.Now(),
	}

	if err := t.store.CreateEvent(e); err != nil {
		t.log.Error("failed to record event", "type", eventType, "error", err)
		return err
	}
	return nil
}
