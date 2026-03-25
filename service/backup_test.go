package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/testutil"
)

// waitForBackupCompletion polls the backup record until it leaves in_progress state.
// CreateBackup spawns a goroutine; we must wait for it to finish before the test
// returns and t.Cleanup closes the DB, otherwise the goroutine panics.
func waitForBackupCompletion(t *testing.T, svc *testutil.ServiceBundle, backupID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := svc.BackupSvc.GetBackup(backupID)
		if err == nil && b != nil && b.Status != "in_progress" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Goroutine may have finished with error or just be slow — either way, it ran
}

func TestBackup_Create_ReturnsInProgressRecord(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Backup Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "test-backup")
	require.NoError(t, err)
	require.NotNil(t, backup)
	assert.NotEmpty(t, backup.ID)
	assert.Equal(t, gs.ID, backup.GameserverID)
	assert.Equal(t, "test-backup", backup.Name)
	assert.Equal(t, "in_progress", backup.Status)
	waitForBackupCompletion(t, svc, backup.ID)
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

	gs := &models.Gameserver{Name: "List Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	b1, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-1")
	require.NoError(t, err)
	b2, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-2")
	require.NoError(t, err)

	waitForBackupCompletion(t, svc, b1.ID)
	waitForBackupCompletion(t, svc, b2.ID)

	list, err := svc.BackupSvc.ListBackups(models.BackupFilter{GameserverID: gs.ID})
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestBackup_Delete_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Del Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "to-delete")
	require.NoError(t, err)
	waitForBackupCompletion(t, svc, backup.ID)

	err = svc.BackupSvc.DeleteBackup(ctx, backup.ID)
	require.NoError(t, err)

	fetched, err := svc.BackupSvc.GetBackup(backup.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched)
}
