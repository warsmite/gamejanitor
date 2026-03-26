package status

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

// StatusSubscriber listens to lifecycle events on the bus and derives gameserver
// status from them. This centralizes status logic in one place instead of 25+
// scattered setGameserverStatus calls.
type StatusSubscriber struct {
	store  Store
	log    *slog.Logger
	bus    *controller.EventBus
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewStatusSubscriber(store Store, bus *controller.EventBus, log *slog.Logger) *StatusSubscriber {
	return &StatusSubscriber{
		store: store,
		bus:   bus,
		log:   log,
	}
}

func (s *StatusSubscriber) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	ch, unsub := s.bus.Subscribe()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				s.handleEvent(event)
			}
		}
	}()

	s.log.Info("status subscriber started")
}

func (s *StatusSubscriber) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.log.Info("status subscriber stopped")
}

func (s *StatusSubscriber) handleEvent(event controller.WebhookEvent) {
	switch e := event.(type) {
	case controller.ImagePullingEvent:
		s.setStatus(e.GameserverID, controller.StatusInstalling, "")
	case controller.ContainerCreatingEvent:
		s.setStatus(e.GameserverID, controller.StatusStarting, "")
	case controller.ContainerStartedEvent:
		s.setStatus(e.GameserverID, controller.StatusStarted, "")
	case controller.GameserverReadyEvent:
		s.setStatus(e.GameserverID, controller.StatusRunning, "")
	case controller.ContainerStoppingEvent:
		s.setStatus(e.GameserverID, controller.StatusStopping, "")
	case controller.ContainerStoppedEvent:
		s.setStatus(e.GameserverID, controller.StatusStopped, "")
	case controller.ContainerExitedEvent:
		s.setStatus(e.GameserverID, controller.StatusError, "Container exited unexpectedly")
	case controller.GameserverErrorEvent:
		s.setStatus(e.GameserverID, controller.StatusError, e.Reason)
	}
}

func (s *StatusSubscriber) setStatus(gameserverID string, newStatus string, errorReason string) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		s.log.Error("status subscriber: failed to get gameserver", "id", gameserverID, "error", err)
		return
	}

	oldStatus := gs.Status
	if oldStatus == newStatus {
		return
	}

	if newStatus != controller.StatusError {
		errorReason = ""
	}

	// Record status as a status_changed activity instead of writing to the gameserver table
	if err := recordStatusActivity(s.store, gameserverID, newStatus, errorReason); err != nil {
		s.log.Error("status subscriber: failed to record status_changed activity", "id", gameserverID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}

	s.log.Info("gameserver status changed", "id", gameserverID, "from", oldStatus, "to", newStatus)

	// Publish derived status_changed event for webhook/SSE consumers
	s.bus.Publish(controller.StatusEvent{
		GameserverID: gameserverID,
		OldStatus:    oldStatus,
		NewStatus:    newStatus,
		ErrorReason:  errorReason,
		Timestamp:    time.Now(),
	})
}

// recordStatusActivity writes a status_changed activity to the activity table.
// This is the single source of truth for gameserver status.
func recordStatusActivity(store Store, gameserverID, newStatus, errorReason string) error {
	data, _ := json.Marshal(map[string]string{
		"new_status":   newStatus,
		"error_reason": errorReason,
	})

	now := time.Now()
	a := &model.Activity{
		ID:           uuid.New().String(),
		GameserverID: &gameserverID,
		Type:         controller.EventStatusChanged,
		Status:       model.ActivityCompleted,
		Actor:        json.RawMessage(`{}`),
		Data:         data,
		StartedAt:    now,
		CompletedAt:  &now,
	}

	return store.CreateActivity(a)
}

