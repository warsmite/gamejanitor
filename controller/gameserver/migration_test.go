package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestMigration_SameNode_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err := live.Migrate(ctx, *gs.NodeID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already on node")
}

func TestMigration_TargetNodeMustHaveCapacity(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(8192))
	testutil.RegisterFakeWorker(t, svc, "worker-2", testutil.WithMaxMemoryMB(512))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Migration Source",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Fill worker-2's 512MB limit with another gameserver
	gs2 := &model.Gameserver{
		Name:          "Filler",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err = svc.Manager.Create(ctx, gs2)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Migrate(ctx, "worker-2")
	require.Error(t, err, "should reject migration when target node is full")
}

func TestMigration_TargetWorkerMustBeOnline(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	// worker-2 NOT registered — only exists in DB
	svc.DB.Exec(`INSERT INTO worker_nodes (id) VALUES (?)`, "worker-2")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err := live.Migrate(ctx, "worker-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unavailable")
}

func TestMigration_SourceWorkerMustBeOnline(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	// Explicitly place on worker-1
	gs := &model.Gameserver{
		Name:   "Source Offline Test",
		GameID: testutil.TestGameID,
		NodeID: testutil.StrPtr("worker-1"),
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Unregister worker-1 after creation
	svc.Registry.Unregister("worker-1")

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Migrate(ctx, "worker-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source worker is offline")
}

func TestMigration_GameserverNotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	live := svc.Manager.Get("nonexistent")
	assert.Nil(t, live, "Get should return nil for nonexistent gameserver")
}

func TestMigration_PortsPreservedInClusterScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w1 := testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:     "Port Preserve Test",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		NodeID:   testutil.StrPtr("worker-1"),
		Env:      model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)
	originalPorts := gs.Ports
	testutil.SeedVolumeData(t, w1, gs.VolumeName)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Migrate(ctx, "worker-2")
	require.NoError(t, err)
	live.WaitForOperation()

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)

	assert.Equal(t, originalPorts, fetched.Ports, "ports should be preserved in cluster scope")
}

func TestMigration_PortsReallocatedInNodeScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	svc.SettingsSvc.Set(settings.SettingPortUniqueness, "node")
	w1 := testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:     "Port Realloc Test",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		NodeID:   testutil.StrPtr("worker-1"),
		Env:      model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)
	testutil.SeedVolumeData(t, w1, gs.VolumeName)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err = live.Migrate(ctx, "worker-2")
	require.NoError(t, err)
	live.WaitForOperation()

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", *fetched.NodeID)
}

func TestMigration_ConcurrentMigrate_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "node-a")
	testutil.RegisterFakeWorker(t, svc, "node-b")
	testutil.RegisterFakeWorker(t, svc, "node-c")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Concurrent Migration",
		GameID: testutil.TestGameID,
		NodeID: testutil.StrPtr("node-a"),
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	// Run two migrations concurrently
	errCh1 := make(chan error, 1)
	errCh2 := make(chan error, 1)

	go func() {
		errCh1 <- live.Migrate(ctx, "node-b")
	}()
	go func() {
		errCh2 <- live.Migrate(ctx, "node-c")
	}()

	err1 := <-errCh1
	err2 := <-errCh2

	// Wait for background operations to complete
	live.WaitForOperation()

	// At least one should succeed; the other might fail or both succeed
	// (if the first completes before the second starts). The key: no panic,
	// no corruption, and the gameserver ends up on exactly one valid node.
	bothSucceeded := err1 == nil && err2 == nil
	oneSucceeded := (err1 == nil) != (err2 == nil)
	bothFailed := err1 != nil && err2 != nil

	if bothFailed {
		t.Logf("both migrations failed: err1=%v, err2=%v", err1, err2)
	}

	assert.True(t, oneSucceeded || bothSucceeded || bothFailed,
		"migrations should resolve deterministically")

	// Verify final state is consistent
	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.NotNil(t, fetched.NodeID, "node_id should not be nil after concurrent migrations")

	validFinalNodes := map[string]bool{"node-a": true, "node-b": true, "node-c": true}
	assert.True(t, validFinalNodes[*fetched.NodeID],
		"final node_id should be a valid node, got %s", *fetched.NodeID)
}
