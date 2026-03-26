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
	gs2, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	gs2.Status = "running"
	store.New(svc.DB).UpdateGameserver(gs2)

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
