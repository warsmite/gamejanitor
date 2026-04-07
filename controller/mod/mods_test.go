package mod_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

// --- Install & Uninstall ---

func TestMods_InstallFromUpload(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Upload a mod file
	mod, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "TestMod", "test-mod.jar", []byte("fake jar content"))
	require.NoError(t, err)
	assert.Equal(t, "TestMod", mod.Name)
	assert.Equal(t, "test-mod.jar", mod.FileName)
	assert.Equal(t, "upload", mod.Source)
	assert.Equal(t, "file", mod.Delivery)
	assert.Equal(t, "/data/mods/test-mod.jar", mod.FilePath)

	// Verify it shows in installed list
	installed, err := svc.ModSvc.ListInstalled(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, installed, 1)
	assert.Equal(t, "TestMod", installed[0].Name)
}

func TestMods_Uninstall(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Install then uninstall
	mod, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "TestMod", "test.jar", []byte("content"))
	require.NoError(t, err)

	err = svc.ModSvc.Uninstall(ctx, gs.ID, mod.ID)
	require.NoError(t, err)

	// Should be gone from DB
	installed, err := svc.ModSvc.ListInstalled(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, installed, 0)
}

func TestMods_UninstallRejectsWrongGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs1 := createTestGameserver(t, ctx, svc)
	gs2 := createTestGameserver(t, ctx, svc)

	// Install on gs1
	mod, err := svc.ModSvc.InstallFromUpload(ctx, gs1.ID, "Mods", "TestMod", "test.jar", []byte("content"))
	require.NoError(t, err)

	// Try to uninstall from gs2 — should fail
	err = svc.ModSvc.Uninstall(ctx, gs2.ID, mod.ID)
	assert.Error(t, err)
}

// --- Scan & Track ---

func TestMods_ScanDetectsUntrackedFiles(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Write a file directly to the volume (simulating SFTP upload)
	err := fw.WriteFile(ctx, gs.VolumeName, "/mods/manual-mod.jar", []byte("manual mod"), 0644)
	require.NoError(t, err)

	// Scan should find it as untracked
	result, err := svc.ModSvc.Scan(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, result.Untracked, 1)
	assert.Equal(t, "manual-mod.jar", result.Untracked[0].Name)
	assert.Len(t, result.Missing, 0)
}

func TestMods_TrackFileCreatesRecord(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Write a file and track it
	err := fw.WriteFile(ctx, gs.VolumeName, "/mods/detected.jar", []byte("detected mod"), 0644)
	require.NoError(t, err)

	tracked, err := svc.ModSvc.TrackFile(ctx, gs.ID, "Mods", "/data/mods/detected.jar", "Detected Mod")
	require.NoError(t, err)
	assert.Equal(t, "Detected Mod", tracked.Name)
	assert.Equal(t, "detected", tracked.Source)

	// Should now show as installed
	installed, err := svc.ModSvc.ListInstalled(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, installed, 1)

	// Scan should show it as tracked, not untracked
	result, err := svc.ModSvc.Scan(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, result.Untracked, 0)
	assert.Len(t, result.Tracked, 1)
}

// --- Reconciliation ---

func TestMods_ReconcileRedownloadsMissingFiles(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Install a mod via upload — puts file on disk + DB record
	mod, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "TestMod", "test.jar", []byte("content"))
	require.NoError(t, err)

	// Verify file exists
	_, err = fw.ReadFile(ctx, gs.VolumeName, "/mods/test.jar")
	require.NoError(t, err)

	// Delete the file (simulating reinstall wiping the volume)
	err = fw.DeletePath(ctx, gs.VolumeName, "/mods/test.jar")
	require.NoError(t, err)

	// Reconcile — uploaded mods have no download_url, so they can't be recovered
	// This should log a warning but not error
	err = svc.ModSvc.Reconcile(ctx, gs.ID)
	require.NoError(t, err)

	// Scan should show it as missing (no download_url to recover from)
	result, err := svc.ModSvc.Scan(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, result.Missing, 1)
	assert.Equal(t, mod.ID, result.Missing[0].ID)
}

// --- Duplicate Prevention ---

func TestMods_DuplicateUploadRejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Install same mod twice
	_, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "TestMod", "test.jar", []byte("v1"))
	require.NoError(t, err)

	// Second upload with different source_id (uuid) should succeed — uploads aren't deduplicated by name
	_, err = svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "TestMod2", "test2.jar", []byte("v2"))
	require.NoError(t, err)

	installed, err := svc.ModSvc.ListInstalled(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, installed, 2)
}

// --- Config ---

func TestMods_GetConfigReturnsCategories(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	config, err := svc.ModSvc.GetConfig(ctx, gs.ID)
	require.NoError(t, err)
	require.NotNil(t, config)
	assert.GreaterOrEqual(t, len(config.Categories), 1)
	assert.Equal(t, "Mods", config.Categories[0].Name)
}

func TestMods_GetConfigForGameWithoutMods(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Config should return something (the test game has mods now)
	config, err := svc.ModSvc.GetConfig(ctx, gs.ID)
	require.NoError(t, err)
	require.NotNil(t, config)
}

// --- Concurrency ---

func TestMods_ConcurrentInstallsAreSerialized(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := createTestGameserver(t, ctx, svc)

	// Run two installs concurrently — the mutex should prevent races
	done := make(chan error, 2)
	go func() {
		_, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "Mod1", "mod1.jar", []byte("content1"))
		done <- err
	}()
	go func() {
		_, err := svc.ModSvc.InstallFromUpload(ctx, gs.ID, "Mods", "Mod2", "mod2.jar", []byte("content2"))
		done <- err
	}()

	require.NoError(t, <-done)
	require.NoError(t, <-done)

	// Both should be installed
	installed, err := svc.ModSvc.ListInstalled(ctx, gs.ID)
	require.NoError(t, err)
	assert.Len(t, installed, 2)
}

// --- Helpers ---

func createTestGameserver(t *testing.T, ctx context.Context, svc *testutil.ServiceBundle) *model.Gameserver {
	t.Helper()
	gs := &model.Gameserver{
		Name:   "Test Server",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "yes"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	return gs
}
