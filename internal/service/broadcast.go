package service

import (
	"sync"
	"time"
)

// Event is a tagged union for all SSE event types.
type Event struct {
	Type string
	Data any
}

type StatusEvent struct {
	GameserverID string    `json:"gameserver_id"`
	OldStatus    string    `json:"old_status"`
	NewStatus    string    `json:"new_status"`
	Timestamp    time.Time `json:"timestamp"`
}

type StatsEvent struct {
	GameserverID  string  `json:"gameserver_id"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryUsageMB int     `json:"memory_usage_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
}

type EventBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[uint64]chan Event
	nextID      uint64
}

func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		subscribers: make(map[uint64]chan Event),
	}
}

// Subscribe returns a channel that receives events and an unsubscribe function.
func (b *EventBroadcaster) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan Event, 64)
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
func (b *EventBroadcaster) Publish(event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// PublishStatus is a convenience method for publishing status change events.
func (b *EventBroadcaster) PublishStatus(event StatusEvent) {
	b.Publish(Event{Type: "status", Data: event})
}

// PublishStats is a convenience method for publishing container stats events.
func (b *EventBroadcaster) PublishStats(event StatsEvent) {
	b.Publish(Event{Type: "stats", Data: event})
}
