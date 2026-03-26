package event

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
	"github.com/google/uuid"
)

// Store is the persistence interface for the event subscriber and history service.
type Store interface {
	CreateEvent(e *model.Event) error
	ListEvents(f model.EventFilter) ([]model.Event, error)
}

// EventStoreSubscriber persists all events from the bus to the database.
type EventStoreSubscriber struct {
	store  Store
	bus    *controller.EventBus
	log    *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewEventStoreSubscriber(store Store, bus *controller.EventBus, log *slog.Logger) *EventStoreSubscriber {
	return &EventStoreSubscriber{store: store, bus: bus, log: log}
}

func (s *EventStoreSubscriber) Start(ctx context.Context) {
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
				s.storeEvent(event)
			}
		}
	}()

	s.log.Info("event store subscriber started")
}

func (s *EventStoreSubscriber) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.log.Info("event store subscriber stopped")
}

func (s *EventStoreSubscriber) storeEvent(event controller.WebhookEvent) {
	// Skip high-frequency telemetry events — served from in-memory cache, not history
	switch event.EventType() {
	case controller.EventGameserverStats, controller.EventGameserverQuery:
		return
	}

	gameserverID := event.EventGameserverID()
	actor := extractActor(event)
	data := extractData(event)

	actorJSON, _ := json.Marshal(actor)
	dataJSON, _ := json.Marshal(data)

	e := &model.Event{
		ID:           uuid.New().String(),
		EventType:    event.EventType(),
		GameserverID: gameserverID,
		Actor:        actorJSON,
		Data:         dataJSON,
		CreatedAt:    event.EventTimestamp(),
	}

	if err := s.store.CreateEvent(e); err != nil {
		s.log.Error("event store: failed to persist event", "event_type", event.EventType(), "error", err)
	}
}

func extractActor(event controller.WebhookEvent) controller.Actor {
	switch e := event.(type) {
	case controller.GameserverActionEvent:
		return e.Actor
	case controller.BackupActionEvent:
		return e.Actor
	case controller.WorkerActionEvent:
		return e.Actor
	case controller.ModActionEvent:
		return e.Actor
	case controller.ScheduleActionEvent:
		return e.Actor
	case controller.ScheduledTaskEvent:
		return e.Actor
	}
	return controller.SystemActor
}

func extractData(event controller.WebhookEvent) any {
	return event
}
