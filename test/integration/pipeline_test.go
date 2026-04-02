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
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

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

	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

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

	// Start writes status synchronously — no polling needed
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

	// Stop writes stopped synchronously via CAS
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "stopped", fetched.Status, "status should be stopped after stop")
}
