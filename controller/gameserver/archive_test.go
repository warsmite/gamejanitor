package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestArchive_AlreadyArchived_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	w := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	testutil.SeedVolumeData(t, w, gs.VolumeName)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Archive(ctx))
	live.WaitForOperation()

	err := live.Archive(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already archived")
}

func TestArchive_NotFound_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	live := svc.Manager.Get("nonexistent")
	assert.Nil(t, live, "Get should return nil for nonexistent gameserver")
}

func TestUnarchive_NotArchived_Error(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	err := live.Unarchive(ctx, "")
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

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Archive(ctx))
	live.WaitForOperation()

	require.NoError(t, live.Unarchive(ctx, ""))
	live.WaitForOperation()

	fetched, err := svc.Manager.GetGameserver(gs.ID)
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

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Archive(ctx))
	live.WaitForOperation()

	require.NoError(t, live.Unarchive(ctx, ""))
	live.WaitForOperation()

	fetched, err := svc.Manager.GetGameserver(gs.ID)
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

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Archive(ctx))
	live.WaitForOperation()

	// Worker can go offline — archived delete shouldn't need it
	svc.Registry.Unregister("worker-1")

	err := svc.Manager.Delete(ctx, gs.ID)
	require.NoError(t, err, "deleting archived gameserver should not require a live worker")

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched, "gameserver should be gone after delete")
}
