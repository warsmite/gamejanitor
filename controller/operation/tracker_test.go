package operation

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

func newTestTracker() *Tracker {
	bus := controller.NewEventBus()
	return NewTracker(bus, slog.Default())
}

func TestTracker_SetAndGet(t *testing.T) {
	tracker := newTestTracker()

	assert.Nil(t, tracker.GetOperation("gs-1"))

	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)

	op := tracker.GetOperation("gs-1")
	require.NotNil(t, op)
	assert.Equal(t, "start", op.Type)
	assert.Equal(t, model.PhaseDownloadingGame, op.Phase)
	assert.Nil(t, op.Progress)
}

func TestTracker_UpdateProgress(t *testing.T) {
	tracker := newTestTracker()
	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)

	tracker.UpdateProgress("gs-1", model.OperationProgress{
		Percent:        45.5,
		CompletedBytes: 100,
		TotalBytes:     220,
	})

	op := tracker.GetOperation("gs-1")
	require.NotNil(t, op)
	require.NotNil(t, op.Progress)
	assert.InDelta(t, 45.5, op.Progress.Percent, 0.01)
	assert.Equal(t, uint64(100), op.Progress.CompletedBytes)
	assert.Equal(t, uint64(220), op.Progress.TotalBytes)
}

func TestTracker_UpdateProgress_NoOp(t *testing.T) {
	tracker := newTestTracker()
	// No operation set — UpdateProgress should be a no-op
	tracker.UpdateProgress("gs-1", model.OperationProgress{Percent: 50})
	assert.Nil(t, tracker.GetOperation("gs-1"))
}

func TestTracker_Clear(t *testing.T) {
	tracker := newTestTracker()
	tracker.SetOperation("gs-1", "start", model.PhaseStarting)

	tracker.ClearOperation("gs-1")
	assert.Nil(t, tracker.GetOperation("gs-1"))

	// Clear again — should be a no-op
	tracker.ClearOperation("gs-1")
}

func TestTracker_PhaseChange(t *testing.T) {
	tracker := newTestTracker()

	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)
	assert.Equal(t, model.PhaseDownloadingGame, tracker.GetOperation("gs-1").Phase)

	tracker.SetOperation("gs-1", "start", model.PhasePullingImage)
	assert.Equal(t, model.PhasePullingImage, tracker.GetOperation("gs-1").Phase)
	assert.Nil(t, tracker.GetOperation("gs-1").Progress) // progress reset on phase change
}

func TestTracker_GetReturnsCopy(t *testing.T) {
	tracker := newTestTracker()
	tracker.SetOperation("gs-1", "start", model.PhaseStarting)

	op1 := tracker.GetOperation("gs-1")
	op1.Phase = "mutated"

	op2 := tracker.GetOperation("gs-1")
	assert.Equal(t, model.PhaseStarting, op2.Phase) // original unchanged
}

func TestTracker_MultipleGameservers(t *testing.T) {
	tracker := newTestTracker()

	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)
	tracker.SetOperation("gs-2", "backup", model.PhaseCreatingBackup)

	assert.Equal(t, "start", tracker.GetOperation("gs-1").Type)
	assert.Equal(t, "backup", tracker.GetOperation("gs-2").Type)

	tracker.ClearOperation("gs-1")
	assert.Nil(t, tracker.GetOperation("gs-1"))
	assert.NotNil(t, tracker.GetOperation("gs-2"))
}

func TestTracker_Watch(t *testing.T) {
	tracker := newTestTracker()

	ch, unwatch := tracker.Watch("gs-1")
	defer unwatch()

	// Set operation — watcher should receive it
	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)

	select {
	case op := <-ch:
		require.NotNil(t, op)
		assert.Equal(t, model.PhaseDownloadingGame, op.Phase)
	case <-time.After(time.Second):
		t.Fatal("watcher did not receive operation")
	}

	// Update progress — watcher should receive it
	tracker.UpdateProgress("gs-1", model.OperationProgress{Percent: 50})

	select {
	case op := <-ch:
		require.NotNil(t, op)
		require.NotNil(t, op.Progress)
		assert.InDelta(t, 50.0, op.Progress.Percent, 0.01)
	case <-time.After(time.Second):
		t.Fatal("watcher did not receive progress")
	}

	// Clear — watcher should receive nil
	tracker.ClearOperation("gs-1")

	select {
	case op := <-ch:
		assert.Nil(t, op)
	case <-time.After(time.Second):
		t.Fatal("watcher did not receive clear")
	}
}

func TestTracker_WatchUnsubscribe(t *testing.T) {
	tracker := newTestTracker()

	ch, unwatch := tracker.Watch("gs-1")
	unwatch()

	tracker.SetOperation("gs-1", "start", model.PhaseStarting)

	// Channel should not receive anything after unwatch
	select {
	case <-ch:
		t.Fatal("received event after unwatch")
	case <-time.After(100 * time.Millisecond):
		// expected
	}
}

func TestTracker_WatchDropsOldValues(t *testing.T) {
	tracker := newTestTracker()

	ch, unwatch := tracker.Watch("gs-1")
	defer unwatch()

	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)

	// Rapid-fire progress updates without consuming
	for i := range 100 {
		tracker.UpdateProgress("gs-1", model.OperationProgress{Percent: float64(i)})
	}

	// Should get the latest value, not all 100
	var lastOp *model.Operation
drain:
	for {
		select {
		case op := <-ch:
			lastOp = op
		default:
			break drain
		}
	}
	require.NotNil(t, lastOp)
	// Should be close to the end, not the beginning
	assert.Greater(t, lastOp.Progress.Percent, float64(50))
}

func TestTracker_Concurrent(t *testing.T) {
	tracker := newTestTracker()

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			gsID := "gs-concurrent"
			tracker.SetOperation(gsID, "start", model.PhaseDownloadingGame)
			for j := range 100 {
				tracker.UpdateProgress(gsID, model.OperationProgress{Percent: float64(j)})
			}
			tracker.GetOperation(gsID)
			tracker.ClearOperation(gsID)
		}(i)
	}
	wg.Wait()
}

func TestTracker_PublishesEvents(t *testing.T) {
	bus := controller.NewEventBus()
	tracker := NewTracker(bus, slog.Default())

	ch, unsub := bus.Subscribe()
	defer unsub()

	tracker.SetOperation("gs-1", "start", model.PhaseDownloadingGame)

	select {
	case ev := <-ch:
		assert.Equal(t, "gameserver.operation", ev.EventType())
		opEv, ok := ev.(controller.OperationEvent)
		require.True(t, ok)
		require.NotNil(t, opEv.Operation)
		assert.Equal(t, model.PhaseDownloadingGame, opEv.Operation.Phase)
	case <-time.After(time.Second):
		t.Fatal("no event published")
	}

	tracker.ClearOperation("gs-1")

	select {
	case ev := <-ch:
		opEv, ok := ev.(controller.OperationEvent)
		require.True(t, ok)
		assert.Nil(t, opEv.Operation)
	case <-time.After(time.Second):
		t.Fatal("no clear event published")
	}
}
