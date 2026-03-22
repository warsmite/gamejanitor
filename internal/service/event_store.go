package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/internal/models"
	"github.com/google/uuid"
)

// EventStoreSubscriber persists all events from the bus to the database.
type EventStoreSubscriber struct {
	db     *sql.DB
	bus    *EventBus
	log    *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewEventStoreSubscriber(db *sql.DB, bus *EventBus, log *slog.Logger) *EventStoreSubscriber {
	return &EventStoreSubscriber{db: db, bus: bus, log: log}
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

func (s *EventStoreSubscriber) storeEvent(event WebhookEvent) {
	gameserverID := extractGameserverID(event)
	actor := extractActor(event)
	data := extractData(event)

	actorJSON, _ := json.Marshal(actor)
	dataJSON, _ := json.Marshal(data)

	e := &models.Event{
		ID:           uuid.New().String(),
		EventType:    event.EventType(),
		GameserverID: gameserverID,
		Actor:        string(actorJSON),
		Data:         string(dataJSON),
		CreatedAt:    event.EventTimestamp(),
	}

	if err := models.CreateEvent(s.db, e); err != nil {
		s.log.Error("event store: failed to persist event", "event_type", event.EventType(), "error", err)
	}
}

func extractGameserverID(event WebhookEvent) string {
	switch e := event.(type) {
	case GameserverEvent:
		return e.GameserverID
	case BackupEvent:
		return e.GameserverID
	case ScheduledTaskEvent:
		return e.GameserverID
	case StatusEvent:
		return e.GameserverID
	case ImagePullingEvent:
		return e.GameserverID
	case ContainerCreatingEvent:
		return e.GameserverID
	case ContainerStartedEvent:
		return e.GameserverID
	case GameserverReadyEvent:
		return e.GameserverID
	case ContainerStoppingEvent:
		return e.GameserverID
	case ContainerStoppedEvent:
		return e.GameserverID
	case ContainerExitedEvent:
		return e.GameserverID
	case GameserverErrorEvent:
		return e.GameserverID
	}
	return ""
}

func extractActor(event WebhookEvent) Actor {
	switch e := event.(type) {
	case GameserverEvent:
		return e.Actor
	case BackupEvent:
		return e.Actor
	case WorkerEvent:
		return e.Actor
	case ScheduledTaskEvent:
		return e.Actor
	}
	return SystemActor
}

func extractData(event WebhookEvent) any {
	return event
}
