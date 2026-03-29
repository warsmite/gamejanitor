package controller

import (
	"log/slog"
	"sync"
	"time"
)

type WebhookEvent interface {
	EventType() string
	EventTimestamp() time.Time
	EventGameserverID() string
	EventActor() Actor
}

type StatusEvent struct {
	GameserverID string    `json:"gameserver_id"`
	OldStatus    string    `json:"old_status"`
	NewStatus    string    `json:"new_status"`
	ErrorReason  string    `json:"error_reason,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

const EventStatusChanged = "status_changed"

func (e StatusEvent) EventType() string        { return EventStatusChanged }
func (e StatusEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e StatusEvent) EventGameserverID() string { return e.GameserverID }
func (e StatusEvent) EventActor() Actor          { return SystemActor }

type EventBus struct {
	mu          sync.RWMutex
	subscribers map[uint64]chan WebhookEvent
	nextID      uint64
}

func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[uint64]chan WebhookEvent),
	}
}

// Subscribe returns a channel that receives events and an unsubscribe function.
func (b *EventBus) Subscribe() (<-chan WebhookEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	// Large buffer so slow subscribers (webhook delivery, SSE clients) don't
	// cause event drops under load. At 1000 gameservers with stats polling,
	// ~300 events/sec flow through the bus. 4096 slots gives ~13 seconds of
	// slack before drops. Memory cost: ~2MB per subscriber (negligible).
	ch := make(chan WebhookEvent, 4096)
	b.subscribers[id] = ch

	unsubscribe := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subscribers, id)
		close(ch)
	}

	return ch, unsubscribe
}

// Publish sends an event to all subscribers. Non-blocking: slow clients miss events.
func (b *EventBus) Publish(event WebhookEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
			slog.Warn("event bus: dropped event, subscriber buffer full", "event_type", event.EventType())
		}
	}
}
