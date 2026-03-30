package integration_test

import (
	"github.com/warsmite/gamejanitor/controller"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

// pollUntil polls a condition with 50ms interval and 15s timeout.
// The long timeout accommodates goroutine scheduling delays when many
// parallel tests compete for CPU. In practice, conditions resolve in <200ms.
func pollUntil(t *testing.T, condition func() bool, msg string) {
	t.Helper()
	require.Eventually(t, condition, 15*time.Second, 50*time.Millisecond, msg)
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
			if evt.EventType() == "status_changed" {
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
