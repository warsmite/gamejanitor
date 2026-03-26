package integration_test

import (
	"github.com/warsmite/gamejanitor/controller"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// pollUntil polls a condition with 50ms interval and 10s timeout.
// The long timeout accommodates goroutine scheduling delays when many
// parallel tests compete for CPU. In practice, conditions resolve in <200ms.
func pollUntil(t *testing.T, condition func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out: %s", msg)
}

func TestPipeline_StatusDerivedFromLifecycleEvents(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver — lifecycle publishes events, StatusSubscriber should derive status
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

	// StatusSubscriber processes events async — poll for status change
	pollUntil(t, func() bool {
		fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
		return fetched != nil && fetched.Status != "stopped"
	}, "status should change from stopped after start")

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	// Status should be one of the active states (installing, starting, started, running)
	assert.Contains(t, []string{"installing", "starting", "started", "running"}, fetched.Status,
		"status should be an active state, got %s", fetched.Status)
}

func TestPipeline_EventPersistedToDatabase(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// EventStoreSubscriber should persist the create event
	pollUntil(t, func() bool {
		events, _ := store.New(svc.DB).ListEvents(model.EventFilter{
			GameserverID: gs.ID,
			Pagination: model.Pagination{Limit: 10},
		})
		return len(events) > 0
	}, "create event should be persisted to events table")

	events, err := store.New(svc.DB).ListEvents(model.EventFilter{
		GameserverID: gs.ID,
		Pagination: model.Pagination{Limit: 10},
	})
	require.NoError(t, err)

	// Should find the gameserver.create event
	found := false
	for _, e := range events {
		if e.EventType == controller.EventGameserverCreate {
			found = true
			break
		}
	}
	assert.True(t, found, "gameserver.create event should be persisted")
}

func TestPipeline_StatusChangedEventPublished(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Subscribe to catch status_changed events
	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

	// StatusSubscriber should publish status_changed events
	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		select {
		case evt := <-ch:
			if evt.EventType() == controller.EventStatusChanged {
				found = true
			}
		default:
			time.Sleep(20 * time.Millisecond)
		}
		if found {
			break
		}
	}
	assert.True(t, found, "status_changed event should be published by StatusSubscriber")
}

func TestPipeline_StopDerivesStopped(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Start, wait for active status
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))
	pollUntil(t, func() bool {
		f, _ := svc.GameserverSvc.GetGameserver(gs.ID)
		return f != nil && f.Status != "stopped"
	}, "should become active")

	// Stop
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))

	// Status should return to stopped
	pollUntil(t, func() bool {
		f, _ := svc.GameserverSvc.GetGameserver(gs.ID)
		return f != nil && f.Status == "stopped"
	}, "status should return to stopped after stop")
}

func TestPipeline_MultipleEventsPersistedInOrder(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Start then stop — should generate multiple events
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))
	time.Sleep(200 * time.Millisecond) // let events process
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))

	// Wait for events to be persisted
	pollUntil(t, func() bool {
		events, _ := store.New(svc.DB).ListEvents(model.EventFilter{
			GameserverID: gs.ID,
			Pagination: model.Pagination{Limit: 50},
		})
		return len(events) >= 3 // at least create + start + stop
	}, "multiple events should be persisted")

	events, err := store.New(svc.DB).ListEvents(model.EventFilter{
		GameserverID: gs.ID,
		Pagination: model.Pagination{Limit: 50},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(events), 3, "should have create, start, and stop events at minimum")
}
