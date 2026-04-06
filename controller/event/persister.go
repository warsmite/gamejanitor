package event

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/model"
)

// skipPersistence contains event types that are too high-frequency to persist.
var skipPersistence = map[string]bool{
	EventGameserverStats:     true,
	EventGameserverQuery:     true,
	EventGameserverOperation: true,
}

// PersistenceStore abstracts event writes for the persister.
type PersistenceStore interface {
	CreateEvent(e *model.Event) error
}

// EventPersister subscribes to the EventBus and writes non-telemetry events
// to the events table. This captures lifecycle phases, errors, worker events,
// schedule events, and mod events that would otherwise be ephemeral.
type EventPersister struct {
	store  PersistenceStore
	bus    *EventBus
	log    *slog.Logger
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewEventPersister(store PersistenceStore, bus *EventBus, log *slog.Logger) *EventPersister {
	return &EventPersister{store: store, bus: bus, log: log}
}

func (p *EventPersister) Start(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	ch, unsub := p.bus.Subscribe()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				p.persist(event)
			}
		}
	}()

	p.log.Info("event persister started")
}

func (p *EventPersister) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
	p.log.Info("event persister stopped")
}

func (p *EventPersister) persist(event WebhookEvent) {
	eventType := event.EventType()

	if skipPersistence[eventType] {
		return
	}

	// Skip events already persisted by trackActivity/recordInstant to avoid duplicates.
	// Those are action events (gameserver.start, gameserver.create, etc.) published
	// from within the gameserver service after writing to the events table.
	// GameserverActionData carries a full Gameserver object — that's the marker.
	if e, ok := event.(Event); ok {
		if _, isAction := e.Data.(*GameserverActionData); isAction {
			return
		}
	}

	gsID := event.EventGameserverID()
	var gsIDPtr *string
	if gsID != "" {
		gsIDPtr = &gsID
	}

	actorJSON, _ := json.Marshal(event.EventActor())
	dataJSON, _ := json.Marshal(event)

	e := &model.Event{
		ID:           uuid.New().String(),
		GameserverID: gsIDPtr,
		Type:         eventType,
		Actor:        actorJSON,
		Data:         dataJSON,
		CreatedAt:    time.Now(),
	}

	if err := p.store.CreateEvent(e); err != nil {
		p.log.Error("failed to persist event", "type", eventType, "error", err)
	}
}
