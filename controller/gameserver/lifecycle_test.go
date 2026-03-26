package gameserver_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)


func TestLifecycle_Start_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.GameserverSvc.Start(testutil.TestContext(), gs.ID)
	require.NoError(t, err)

	assert.Greater(t, fw.ContainerCount(), 0, "should have created a container")

	// Verify gameserver has container ID in DB
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.NotNil(t, fetched.ContainerID)
}

func TestLifecycle_Stop_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// Start it first
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Then stop — verify it completes without error
	err := svc.GameserverSvc.Stop(testutil.TestContext(), gs.ID)
	require.NoError(t, err)
}

func TestLifecycle_Start_AlreadyRunning_Noop(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Manually set status to running to simulate the status subscriber
	testutil.SetGameserverStatus(t, store.New(svc.DB), gs.ID, "running")

	// Starting again should be a no-op
	err := svc.GameserverSvc.Start(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)
}

func TestLifecycle_Start_PullImageFailure(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	fw.FailNext("PullImage", fmt.Errorf("network timeout"))

	err := svc.GameserverSvc.Start(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pulling image")
}

func TestLifecycle_Start_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	err := svc.GameserverSvc.Start(testutil.TestContext(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLifecycle_Stop_AlreadyStopped_Noop(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)
	// Gameserver starts as "stopped" — stopping again should be a no-op
	err := svc.GameserverSvc.Stop(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)
}

func TestLifecycle_Start_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker so it becomes unavailable
	_ = fw
	svc.Registry.Unregister("worker-1")

	err := svc.GameserverSvc.Start(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}

func TestLifecycle_Stop_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start it first
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Set status to running so stop doesn't short-circuit
	testutil.SetGameserverStatus(t, store.New(svc.DB), gs.ID, "running")

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	// Stop should still succeed — the lifecycle code logs a warning but
	// proceeds with clearing the container ID and completing the stop.
	err := svc.GameserverSvc.Stop(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)
}

func TestLifecycle_Restart_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	// Restart requires starting, which needs a worker
	err := svc.GameserverSvc.Restart(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}
