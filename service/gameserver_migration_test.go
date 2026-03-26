package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestMigration_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	// Explicitly place on worker-1 so we know which node to migrate from
	gs := &model.Gameserver{
		Name:   "Migration Test",
		GameID: testutil.TestGameID,
		NodeID: testutil.StrPtr("worker-1"),
		Env:    []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	require.Equal(t, "worker-1", *gs.NodeID)

	err = svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.NodeID)
	assert.Equal(t, "worker-2", *fetched.NodeID)
}

func TestMigration_SameNode_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, *gs.NodeID)
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
		Env:           []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Fill worker-2's 512MB limit with another gameserver
	gs2 := &model.Gameserver{
		Name:          "Filler",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 512,
		Env:           []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)

	err = svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
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

	err := svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
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
		Env:    []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Unregister worker-1 after creation
	svc.Registry.Unregister("worker-1")

	err = svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source worker is offline")
}

func TestMigration_GameserverNotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	err := svc.GameserverSvc.MigrateGameserver(ctx, "nonexistent", "worker-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestMigration_PortsPreservedInClusterScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:     "Port Preserve Test",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		NodeID:   testutil.StrPtr("worker-1"),
		Env:      []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	originalPorts := string(gs.Ports)

	err = svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)

	assert.Equal(t, originalPorts, string(fetched.Ports), "ports should be preserved in cluster scope")
}

func TestMigration_PortsReallocatedInNodeScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	svc.SettingsSvc.Set(service.SettingPortUniqueness, "node")
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:     "Port Realloc Test",
		GameID:   testutil.TestGameID,
		PortMode: "auto",
		NodeID:   testutil.StrPtr("worker-1"),
		Env:      []byte(`{"REQUIRED_VAR":"v"}`),
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	err = svc.GameserverSvc.MigrateGameserver(ctx, gs.ID, "worker-2")
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", *fetched.NodeID)
}
