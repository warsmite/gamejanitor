package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestFileService_ListDirectory_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Write a file to the fake worker's volume
	require.NoError(t, fw.WriteFile(ctx, gs.VolumeName, "/test.txt", []byte("hello"), 0644))

	entries, err := svc.FileSvc.ListDirectory(ctx, gs.ID, "/data")
	require.NoError(t, err)

	found := false
	for _, e := range entries {
		if e.Name == "test.txt" {
			found = true
		}
	}
	assert.True(t, found, "test.txt should appear in listing")
}

func TestFileService_ReadWriteFile(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.FileSvc.WriteFile(ctx, gs.ID, "/data/config.txt", []byte("setting=value")))

	data, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/data/config.txt")
	require.NoError(t, err)
	assert.Equal(t, "setting=value", string(data))
}

func TestFileService_DeletePath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.FileSvc.WriteFile(ctx, gs.ID, "/data/delete-me.txt", []byte("gone")))
	require.NoError(t, svc.FileSvc.DeletePath(ctx, gs.ID, "/data/delete-me.txt"))

	_, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/data/delete-me.txt")
	assert.Error(t, err)
}

func TestFileService_DeleteRoot_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.FileSvc.DeletePath(ctx, gs.ID, "/data")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot delete the root")
}

func TestFileService_CreateDirectory(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.FileSvc.CreateDirectory(ctx, gs.ID, "/data/subdir"))

	entries, err := svc.FileSvc.ListDirectory(ctx, gs.ID, "/data")
	require.NoError(t, err)

	found := false
	for _, e := range entries {
		if e.Name == "subdir" && e.IsDir {
			found = true
		}
	}
	assert.True(t, found, "subdir should appear as directory")
}

func TestFileService_RenamePath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.FileSvc.WriteFile(ctx, gs.ID, "/data/old.txt", []byte("content")))
	require.NoError(t, svc.FileSvc.RenamePath(ctx, gs.ID, "/data/old.txt", "/data/new.txt"))

	_, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/data/old.txt")
	assert.Error(t, err)

	data, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/data/new.txt")
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
}

func TestFileService_RenameRoot_Rejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.FileSvc.RenamePath(ctx, gs.ID, "/data", "/data/other")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot rename the root")
}

func TestFileService_PathValidation_Traversal(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	_, err := svc.FileSvc.ReadFile(ctx, gs.ID, "/etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be within /data")

	_, err = svc.FileSvc.ReadFile(ctx, gs.ID, "../../etc/passwd")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be within /data")
}

func TestFileService_PathValidation_MustStartWithData(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.FileSvc.WriteFile(ctx, gs.ID, "/tmp/evil.txt", []byte("bad"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be within /data")
}

func TestFileService_GameserverNotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	_, err := svc.FileSvc.ReadFile(ctx, "nonexistent", "/data/file.txt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
