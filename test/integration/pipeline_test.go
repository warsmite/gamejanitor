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

	// Start writes status synchronously via CAS
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	// Status should be one of the active or terminal states (fake worker completes instantly)
	assert.Contains(t, []string{"installing", "starting", "started", "running", "error"}, fetched.Status,
		"status should not still be stopped after start, got %s", fetched.Status)
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
