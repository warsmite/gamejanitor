package integration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestPipeline_StatusDerivedFromLifecycleEvents(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Start triggers the lifecycle; worker state arrives asynchronously via the event stream
	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID))

	// Poll until the worker-reported state is reflected in DeriveStatus
	activeStatuses := []string{"installing", "starting", "running", "error"}
	deadline := time.Now().Add(3 * time.Second)
	var lastStatus string
	for time.Now().Before(deadline) {
		fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
		require.NoError(t, err)
		lastStatus = fetched.Status
		for _, s := range activeStatuses {
			if lastStatus == s {
				return // success
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status should be one of %v after start, got %s", activeStatuses, lastStatus)
}

func TestPipeline_StatusChangedEventPublished(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID))

	deadline := time.Now().Add(3 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		select {
		case evt := <-ch:
			if evt.EventType() == "gameserver.ready" {
				found = true
			}
		default:
			time.Sleep(20 * time.Millisecond)
		}
		if found {
			break
		}
	}
	assert.True(t, found, "gameserver.ready event should be published during start")
}

func TestPipeline_StopDerivesStopped(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Poll until DeriveStatus reflects the stopped state. The worker event
	// stream is async — stale "running" events from Start may arrive after
	// Stop clears the worker state cache.
	deadline := time.Now().Add(3 * time.Second)
	var lastStatus string
	for time.Now().Before(deadline) {
		fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
		require.NoError(t, err)
		lastStatus = fetched.Status
		if lastStatus == "stopped" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status should be stopped after stop, got %s", lastStatus)
}
