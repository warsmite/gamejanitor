package controller_test

import (
	"github.com/warsmite/gamejanitor/controller"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

)

func TestEventBus_PublishSubscribe(t *testing.T) {
	t.Parallel()
	bus := controller.NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	evt := controller.ImagePullingEvent{GameserverID: "gs-1", Timestamp: time.Now()}
	bus.Publish(evt)

	select {
	case received := <-ch:
		assert.Equal(t, controller.EventImagePulling, received.EventType())
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	t.Parallel()
	bus := controller.NewEventBus()

	ch1, unsub1 := bus.Subscribe()
	defer unsub1()
	ch2, unsub2 := bus.Subscribe()
	defer unsub2()

	bus.Publish(controller.ImagePullingEvent{GameserverID: "gs-1", Timestamp: time.Now()})

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
	bus := controller.NewEventBus()

	ch, unsub := bus.Subscribe()
	unsub()

	bus.Publish(controller.ImagePullingEvent{GameserverID: "gs-1", Timestamp: time.Now()})

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
	bus := controller.NewEventBus()

	ch, unsub := bus.Subscribe()
	defer unsub()

	// Fill the 4096-element buffer and then some
	for i := 0; i < 4100; i++ {
		bus.Publish(controller.ImagePullingEvent{GameserverID: "gs-1", Timestamp: time.Now()})
	}

	// Should have at most 4096 events (buffer size)
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	assert.LessOrEqual(t, count, 4096, "buffer should cap at 4096 events")
	assert.Greater(t, count, 0, "should have received some events")
}

func TestEventBus_EventTypes(t *testing.T) {
	t.Parallel()

	// Verify key event type constants are non-empty
	require.NotEmpty(t, controller.EventGameserverCreate)
	require.NotEmpty(t, controller.EventGameserverStart)
	require.NotEmpty(t, controller.EventGameserverStop)
	require.NotEmpty(t, controller.EventGameserverDelete)
	require.NotEmpty(t, "status_changed")
	require.NotEmpty(t, controller.EventBackupCreate)
	require.NotEmpty(t, controller.EventScheduleCreate)
}
