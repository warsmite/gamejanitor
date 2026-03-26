package gameserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/model"
)

type activityContextKey struct{}

// WithActivityID attaches an activity ID to the context.
// Inner operations (e.g. Stop/Start within Restart) check this to avoid
// creating nested activities.
func WithActivityID(ctx context.Context, activityID string) context.Context {
	return context.WithValue(ctx, activityContextKey{}, activityID)
}

// ActivityIDFromContext returns the activity ID from context, if any.
func ActivityIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(activityContextKey{}).(string); ok {
		return v
	}
	return ""
}

// ActivityStore abstracts activity persistence.
type ActivityStore interface {
	CreateActivity(a *model.Activity) error
	CompleteActivity(id string) error
	FailActivity(id string, errMsg string) error
	HasRunningActivity(gameserverID string) (bool, error)
}

// ActivityTracker manages the lifecycle of activities.
// Shared between GameserverService and BackupService.
type ActivityTracker struct {
	store ActivityStore
	log   *slog.Logger
}

func NewActivityTracker(store ActivityStore, log *slog.Logger) *ActivityTracker {
	return &ActivityTracker{store: store, log: log}
}

// Start records a new running activity. Returns the activity ID.
// Returns an error if the gameserver already has a running activity
// (unless the new activity is a stop — you should always be able to stop).
func (t *ActivityTracker) Start(gameserverID, workerID, activityType string, actor json.RawMessage, data json.RawMessage) (string, error) {
	if activityType != model.OpStop {
		busy, err := t.store.HasRunningActivity(gameserverID)
		if err != nil {
			return "", err
		}
		if busy {
			return "", fmt.Errorf("gameserver %s already has an operation in progress", gameserverID)
		}
	}

	if actor == nil {
		actor = json.RawMessage(`{}`)
	}
	if data == nil {
		data = json.RawMessage(`{}`)
	}

	a := &model.Activity{
		ID:           uuid.New().String(),
		GameserverID: &gameserverID,
		WorkerID:     workerID,
		Type:         activityType,
		Status:       model.ActivityRunning,
		Actor:        actor,
		Data:         data,
		StartedAt:    time.Now(),
	}

	if err := t.store.CreateActivity(a); err != nil {
		t.log.Error("failed to create activity record", "type", activityType, "gameserver_id", gameserverID, "error", err)
		return "", err
	}

	t.log.Info("activity started", "activity_id", a.ID, "type", activityType, "gameserver_id", gameserverID, "worker_id", workerID)
	return a.ID, nil
}

// Complete marks an activity as completed.
func (t *ActivityTracker) Complete(activityID string) {
	if activityID == "" {
		return
	}
	if err := t.store.CompleteActivity(activityID); err != nil {
		t.log.Error("failed to complete activity", "activity_id", activityID, "error", err)
	}
}

// Fail marks an activity as failed with an error message.
func (t *ActivityTracker) Fail(activityID string, reason error) {
	if activityID == "" {
		return
	}
	errMsg := ""
	if reason != nil {
		errMsg = reason.Error()
	}
	if err := t.store.FailActivity(activityID, errMsg); err != nil {
		t.log.Error("failed to fail activity", "activity_id", activityID, "error", err)
	}
}

// RecordInstant creates and immediately completes an activity for events that
// have no lifecycle (CRUD operations, status changes).
func (t *ActivityTracker) RecordInstant(gameserverID *string, activityType string, actor json.RawMessage, data json.RawMessage) error {
	if actor == nil {
		actor = json.RawMessage(`{}`)
	}
	if data == nil {
		data = json.RawMessage(`{}`)
	}

	now := time.Now()
	a := &model.Activity{
		ID:           uuid.New().String(),
		GameserverID: gameserverID,
		Type:         activityType,
		Status:       model.ActivityCompleted,
		Actor:        actor,
		Data:         data,
		StartedAt:    now,
		CompletedAt:  &now,
	}

	if err := t.store.CreateActivity(a); err != nil {
		t.log.Error("failed to record instant activity", "type", activityType, "error", err)
		return err
	}
	return nil
}
