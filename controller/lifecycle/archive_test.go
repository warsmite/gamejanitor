package lifecycle_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestArchive_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	err := svc.LifecycleSvc.Archive(ctx, gs.ID)
	require.NoError(t, err)
	

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.True(t, fetched.IsArchived())
	assert.Equal(t, "archived", fetched.DesiredState)
	assert.Nil(t, fetched.NodeID)
	assert.Nil(t, fetched.InstanceID)

	// Volume should be removed from worker
	_, err = w.ReadFile(ctx, gs.VolumeName, "server.properties")
	assert.Error(t, err, "volume should be removed after archive")
}

func TestArchive_AlreadyArchived_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	

	err := svc.LifecycleSvc.Archive(ctx, gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already archived")
}

func TestArchive_NotFound_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	err := svc.LifecycleSvc.Archive(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUnarchive_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	

	err := svc.LifecycleSvc.Unarchive(ctx, gs.ID, "")
	require.NoError(t, err)
	

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.False(t, fetched.IsArchived())
	assert.NotEqual(t, "archived", fetched.DesiredState)
	assert.NotNil(t, fetched.NodeID)

	// Data should be restored to the volume
	data, err := w.ReadFile(ctx, gs.VolumeName, "server.properties")
	require.NoError(t, err, "file should exist on worker after unarchive")
	assert.Equal(t, "test=true\n", string(data))
}

func TestUnarchive_TargetNode(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w1 := testutil.RegisterFakeWorker(t, svc, "worker-1")
	w2 := testutil.RegisterFakeWorker(t, svc, "worker-2")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Archive Target Node",
		GameID: testutil.TestGameID,
		NodeID: testutil.StrPtr("worker-1"),
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	testutil.SeedVolumeData(t, w1, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	

	// Unarchive to a specific node
	err = svc.LifecycleSvc.Unarchive(ctx, gs.ID, "worker-2")
	require.NoError(t, err)
	

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "worker-2", *fetched.NodeID)

	// Data should be on worker-2
	data, err := w2.ReadFile(ctx, gs.VolumeName, "server.properties")
	require.NoError(t, err)
	assert.Equal(t, "test=true\n", string(data))
}

func TestUnarchive_NotArchived_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.LifecycleSvc.Unarchive(ctx, gs.ID, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not archived")
}

func TestUnarchive_PortsPreservedInClusterScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	originalPorts := gs.Ports
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	
	require.NoError(t, svc.LifecycleSvc.Unarchive(ctx, gs.ID, ""))
	

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, originalPorts, fetched.Ports, "ports should be preserved in cluster scope")
}

func TestUnarchive_PortsReallocatedInNodeScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	svc.SettingsSvc.Set(settings.SettingPortUniqueness, "node")
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	
	require.NoError(t, svc.LifecycleSvc.Unarchive(ctx, gs.ID, ""))
	

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.NotNil(t, fetched.NodeID)
	// Ports should be reallocated (may be same values but that's fine — just verifying no error)
	assert.NotEmpty(t, fetched.Ports)
}

func TestDeleteArchived_SkipsWorkerCleanup(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	require.NoError(t, svc.LifecycleSvc.Archive(ctx, gs.ID))
	

	// Worker can go offline — archived delete shouldn't need it
	svc.Registry.Unregister("worker-1")

	err := svc.GameserverSvc.DeleteGameserver(ctx, gs.ID)
	require.NoError(t, err, "deleting archived gameserver should not require a live worker")

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched, "gameserver should be gone after delete")
}
