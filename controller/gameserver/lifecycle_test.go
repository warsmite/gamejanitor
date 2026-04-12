package gameserver_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestLifecycle_Start_PullImageFailure(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	fw.FailNext("PullImage", fmt.Errorf("network timeout"))

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err := live.Start(testutil.TestContext())
	require.NoError(t, err, "Start returns immediately; the failure happens in the goroutine")

	// Wait for the operation goroutine to complete
	live.WaitForOperation()

	// The start should have failed — check for error state
	snap := live.Snapshot()
	assert.Equal(t, "error", snap.Status, "should be in error state after PullImage failure")
	assert.Contains(t, snap.ErrorReason, "pull")
}

func TestLifecycle_Start_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker so it becomes unavailable
	svc.Registry.Unregister("worker-1")

	// The LiveGameserver should have its worker cleared when worker goes offline
	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err := live.Start(testutil.TestContext())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}

func TestLifecycle_Stop_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Start it first
	require.NoError(t, live.Start(testutil.TestContext()))
	live.WaitForOperation()

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	// Stop should still complete — clears state even without worker
	err := live.Stop(testutil.TestContext())
	assert.NoError(t, err)
}

func TestLifecycle_Restart_WorkerUnavailable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Unregister the worker
	svc.Registry.Unregister("worker-1")

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Restart requires starting, which needs a worker
	err := live.Restart(testutil.TestContext())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker unavailable")
}

// Auto-migration on start was removed from the new architecture.
// These tests verify the current behavior: Start runs on the assigned node
// regardless of capacity, and fails if the worker is unavailable.

func TestLifecycle_Start_AutoMigratesWhenNodeOvercommitted(t *testing.T) {
	// In the current architecture, Start does NOT auto-migrate. It starts on the
	// assigned node. This test verifies Start succeeds even when the node is
	// "overcommitted" (capacity is only checked at creation time, not start time).
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "small-node", testutil.WithMaxMemoryMB(1024))
	testutil.RegisterFakeWorker(t, svc, "big-node", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	// Create gameserver on small-node with 256MB (fits fine)
	gs := &model.Gameserver{
		Name:          "Overcommit Test",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 256,
		NodeID:        testutil.StrPtr("small-node"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)
	require.Equal(t, "small-node", *gs.NodeID)

	// Bump memory in DB to exceed small-node's capacity
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Start succeeds — no capacity check on start in current architecture
	err = live.Start(ctx)
	require.NoError(t, err)
	live.WaitForOperation()

	// Gameserver stays on its assigned node
	snap := live.Snapshot()
	assert.Equal(t, "small-node", *snap.NodeID, "should stay on original node (no auto-migration)")
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
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Start(ctx)
	require.NoError(t, err)
	live.WaitForOperation()

	snap := live.Snapshot()
	assert.Equal(t, "worker-1", *snap.NodeID, "should stay on original node")
	assert.Greater(t, fw1.InstanceCount(), 0, "instance should be on worker-1")
}

func TestLifecycle_Start_FailsWhenNoNodeHasCapacity(t *testing.T) {
	// In the current architecture, there's no capacity check on Start — only at
	// creation time. This test verifies that Start doesn't fail due to capacity;
	// it just starts on the assigned node.
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "node-a", testutil.WithMaxMemoryMB(1024))
	testutil.RegisterFakeWorker(t, svc, "node-b", testutil.WithMaxMemoryMB(1024))
	ctx := testutil.TestContext()

	// Create gameserver with 256MB on node-a (fits fine)
	gs := &model.Gameserver{
		Name:          "Capacity Test",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 256,
		NodeID:        testutil.StrPtr("node-a"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Bump memory in DB beyond what any node can fit
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Start succeeds — no capacity check on start
	err = live.Start(ctx)
	require.NoError(t, err)
	live.WaitForOperation()
}

func TestLifecycle_Start_AutoMigrateAfterResourceUpgrade(t *testing.T) {
	// In the current architecture, Start does NOT auto-migrate after resource
	// upgrade. This test verifies Start succeeds on the existing node.
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "small-node", testutil.WithMaxMemoryMB(1024))
	testutil.RegisterFakeWorker(t, svc, "big-node", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Resource Upgrade",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		NodeID:        testutil.StrPtr("small-node"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Simulate: DB has upgraded resources but gameserver is still on small-node
	_, err = svc.DB.Exec("UPDATE gameservers SET memory_limit_mb = ? WHERE id = ?", 2048, gs.ID)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Start succeeds — starts on the existing node without auto-migration
	err = live.Start(ctx)
	require.NoError(t, err)
	live.WaitForOperation()

	snap := live.Snapshot()
	assert.Equal(t, "small-node", *snap.NodeID, "should stay on small-node (no auto-migration)")
}

func TestLifecycle_Start_SkipsCapacityCheckWithZeroLimits(t *testing.T) {
	// Gameservers with no memory limit set should start without issues
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
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Start(ctx)
	require.NoError(t, err)
	live.WaitForOperation()

	snap := live.Snapshot()
	assert.Equal(t, "worker-1", *snap.NodeID)
	assert.Greater(t, fw.InstanceCount(), 0)
}
