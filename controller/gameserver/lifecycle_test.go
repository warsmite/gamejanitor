package gameserver_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/worker"
)


func TestLifecycle_Start_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	assert.Greater(t, fw.InstanceCount(), 0, "should have created an instance")

	// Verify gameserver has instance ID in DB
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.NotNil(t, fetched.InstanceID)
}

func TestLifecycle_Stop_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// Start it first
	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Then stop — verify it completes without error
	err := svc.LifecycleSvc.Stop(testutil.TestContext(), gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()
}

func TestLifecycle_Start_AlreadyRunning_Noop(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)
	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Inject worker state to simulate the status subscriber receiving a running event
	svc.StatusMgr.InjectWorkerState(gs.ID, &worker.InstanceStateUpdate{State: worker.StateRunning})

	// Starting again should be a no-op
	err := svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)
}

func TestLifecycle_Start_PullImageFailure(t *testing.T) {
	t.Parallel()
	// Use subscribers so the StatusManager picks up GameserverErrorEvent
	svc := testutil.NewTestServicesWithSubscribers(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	fw.FailNext("PullImage", fmt.Errorf("network timeout"))

	// Start returns nil (dispatches async), but the background goroutine fails
	err := svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	// Poll until the StatusManager processes the error event from the EventBus
	deadline := time.Now().Add(3 * time.Second)
	var fetched *model.Gameserver
	for time.Now().Before(deadline) {
		fetched, err = svc.GameserverSvc.GetGameserver(gs.ID)
		require.NoError(t, err)
		if fetched.Status == "error" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.Equal(t, "error", fetched.Status)
	assert.Contains(t, fetched.ErrorReason, "pull game image")
}

func TestLifecycle_Start_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	err := svc.LifecycleSvc.Start(testutil.TestContext(), "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestLifecycle_Stop_AlreadyStopped_Noop(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)
	// Gameserver starts as "stopped" — stopping again should complete without error
	err := svc.LifecycleSvc.Stop(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()
}

func TestLifecycle_Start_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker so it becomes unavailable
	_ = fw
	svc.Registry.Unregister("worker-1")

	err := svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}

func TestLifecycle_Stop_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start it first
	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Inject worker state so stop sees the gameserver as running
	svc.StatusMgr.InjectWorkerState(gs.ID, &worker.InstanceStateUpdate{State: worker.StateRunning})

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	// Stop should still succeed — the lifecycle code logs a warning but
	// proceeds with clearing the instance ID and completing the stop.
	err := svc.LifecycleSvc.Stop(testutil.TestContext(), gs.ID)
	assert.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()
}

func TestLifecycle_Restart_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	// Restart requires starting, which needs a worker
	err := svc.LifecycleSvc.Restart(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}

// ── Multi-node / auto-migration on start ──

func TestLifecycle_Start_AutoMigratesWhenNodeOvercommitted(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw1 := testutil.RegisterFakeWorker(t, svc, "small-node", testutil.WithMaxMemoryMB(1024))
	fw2 := testutil.RegisterFakeWorker(t, svc, "big-node", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	// Create gameserver on small-node with 256MB (fits fine)
	gs := &model.Gameserver{
		Name:          "Auto Migrate Test",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 256,
		NodeID:        testutil.StrPtr("small-node"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	require.Equal(t, "small-node", *gs.NodeID)
	testutil.SeedVolumeData(t, fw1, gs.VolumeName)

	// Bump memory in DB to exceed small-node's capacity (simulates resource
	// upgrade where async migration failed, or admin reduced node capacity)
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	// Start should detect overcommit and auto-migrate to big-node
	err = svc.LifecycleSvc.Start(ctx, gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "big-node", *fetched.NodeID, "should have auto-migrated to big-node")
	assert.Greater(t, fw2.InstanceCount(), 0, "instance should be on big-node")
}

func TestLifecycle_Start_NoMigrationNeededWhenNodeHasCapacity(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw1 := testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(8192))
	testutil.RegisterFakeWorker(t, svc, "worker-2", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "No Migration Needed",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		NodeID:        testutil.StrPtr("worker-1"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	err = svc.LifecycleSvc.Start(ctx, gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker-1", *fetched.NodeID, "should stay on original node")
	assert.Greater(t, fw1.InstanceCount(), 0, "instance should be on worker-1")
}

func TestLifecycle_Start_FailsWhenNoNodeHasCapacity(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "node-a", testutil.WithMaxMemoryMB(1024))
	testutil.RegisterFakeWorker(t, svc, "node-b", testutil.WithMaxMemoryMB(1024))
	ctx := testutil.TestContext()

	// Create gameserver with 256MB on node-a (fits fine)
	gs := &model.Gameserver{
		Name:          "No Capacity Anywhere",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 256,
		NodeID:        testutil.StrPtr("node-a"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Bump memory in DB beyond what any node can fit
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	err = svc.LifecycleSvc.Start(ctx, gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lacks capacity")
}

func TestLifecycle_Start_AutoMigrateAfterResourceUpgrade(t *testing.T) {
	// Simulates the failed auto-migration race: resources updated in DB but
	// migration failed. Next start should auto-migrate instead of overcommitting.
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw1 := testutil.RegisterFakeWorker(t, svc, "small-node", testutil.WithMaxMemoryMB(1024))
	testutil.RegisterFakeWorker(t, svc, "big-node", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Resource Upgrade Migration",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		NodeID:        testutil.StrPtr("small-node"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	testutil.SeedVolumeData(t, fw1, gs.VolumeName)

	// Simulate: DB has upgraded resources but gameserver is still on small-node
	// (as if auto-migration on update failed)
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	// Start should detect overcommit and auto-migrate
	err = svc.LifecycleSvc.Start(ctx, gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "big-node", *fetched.NodeID, "should have migrated to big-node")
}

func TestLifecycle_Start_SkipsCapacityCheckWithZeroLimits(t *testing.T) {
	// Gameservers with no memory limit set should start without capacity checks
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(1024))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "No Limits",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 0,
		NodeID:        testutil.StrPtr("worker-1"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	err = svc.LifecycleSvc.Start(ctx, gs.ID)
	require.NoError(t, err)

	svc.LifecycleSvc.WaitForOperations()

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker-1", *fetched.NodeID)
	assert.Greater(t, fw.InstanceCount(), 0)
}
