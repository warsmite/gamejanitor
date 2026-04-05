package backup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestBackup_Create_ReturnsInProgressRecord(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "Backup Host", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "test-backup")
	require.NoError(t, err)
	require.NotNil(t, backup)
	assert.NotEmpty(t, backup.ID)
	assert.Equal(t, gs.ID, backup.GameserverID)
	assert.Equal(t, "test-backup", backup.Name)
	assert.Equal(t, "in_progress", backup.Status)
	testutil.WaitForBackupCompletion(t, svc, backup.ID)
}

func TestBackup_Create_GameserverNotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	ctx := testutil.TestContext()

	_, err := svc.BackupSvc.CreateBackup(ctx, "nonexistent", "test-backup")
	require.Error(t, err)
}

func TestBackup_List_ByGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "List Host", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	b1, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-1")
	require.NoError(t, err)
	b2, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-2")
	require.NoError(t, err)

	testutil.WaitForBackupCompletion(t, svc, b1.ID)
	testutil.WaitForBackupCompletion(t, svc, b2.ID)

	list, err := svc.BackupSvc.ListBackups(model.BackupFilter{GameserverID: gs.ID})
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestBackup_Delete_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "Del Host", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "to-delete")
	require.NoError(t, err)
	testutil.WaitForBackupCompletion(t, svc, backup.ID)

	err = svc.BackupSvc.DeleteBackup(ctx, gs.ID, backup.ID)
	require.NoError(t, err)

	fetched, err := svc.BackupSvc.GetBackup(gs.ID, backup.ID)
	require.Error(t, err)
	assert.Nil(t, fetched)
}
