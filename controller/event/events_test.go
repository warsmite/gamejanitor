package event_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/event"
)

func TestEventBus_PublishSubscribe(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	evt := event.NewSystemEvent(event.EventImagePulling, "gs-1", nil)
	bus.Publish(evt)

	select {
	case received := <-ch:
		assert.Equal(t, event.EventImagePulling, received.EventType())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus()

	ch1, unsub1 := bus.Subscribe()
	defer unsub1()
	ch2, unsub2 := bus.Subscribe()
	defer unsub2()

	bus.Publish(event.NewSystemEvent(event.EventImagePulling, "gs-1", nil))

	select {
	case <-ch1:
	case <-time.After(time.Second):
		t.Fatal("subscriber 1 didn't receive event")
	}
	select {
	case <-ch2:
	case <-time.After(time.Second):
		t.Fatal("subscriber 2 didn't receive event")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus()

	ch, unsub := bus.Subscribe()
	unsub()

	bus.Publish(event.NewSystemEvent(event.EventImagePulling, "gs-1", nil))

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("should not receive events after unsubscribe")
		}
	default:
		// Channel closed or empty — expected
	}
}

func TestEventBus_SlowSubscriber_EventDropped(t *testing.T) {
	t.Parallel()
	bus := event.NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	// Fill the 4096-element buffer and then some
	for i := 0; i < 4100; i++ {
		bus.Publish(event.NewSystemEvent(event.EventImagePulling, "gs-1", nil))
	}

	// Should have at most 4096 events (buffer size)
	count := 0
drain:
	for {
		select {
		case <-ch:
			count++
		default:
			break drain
		}
	}
	assert.LessOrEqual(t, count, 4096, "buffer should cap at 4096 events")
	assert.Greater(t, count, 0, "should have received some events")
}

func TestEventBus_EventTypes(t *testing.T) {
	t.Parallel()

	// Verify key event type constants are non-empty
	require.NotEmpty(t, event.EventGameserverCreate)
	require.NotEmpty(t, event.EventGameserverStart)
	require.NotEmpty(t, event.EventGameserverStop)
	require.NotEmpty(t, event.EventGameserverDelete)
	require.NotEmpty(t, event.EventBackupCreate)
	require.NotEmpty(t, event.EventScheduleCreate)
}
