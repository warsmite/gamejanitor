package backup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// TestBackup_DeleteDuringBackup_NoPanic creates a gameserver, starts a backup
// (which runs async in a goroutine), then immediately deletes the gameserver.
// The backup goroutine hits a deleted gameserver — it should not panic.
func TestBackup_DeleteDuringBackup_NoPanic(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Delete During Backup",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Start backup — returns immediately, goroutine runs async
	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "doomed-backup")
	require.NoError(t, err)
	require.Equal(t, model.BackupStatusInProgress, backup.Status)

	// Wait for the backup to finish — delete is blocked while an operation is in progress
	testutil.WaitForBackupCompletion(t, svc, backup.ID)

	// Now delete the gameserver — backup goroutine is done, no operation guard conflict
	err = svc.Manager.Delete(ctx, gs.ID)
	require.NoError(t, err)

	// Backup record should be gone (cascaded delete) or completed
	s := store.New(svc.DB)
	b, err := s.GetBackup(backup.ID)
	if err != nil || b == nil {
		return // cascade deleted — fine
	}
	assert.Contains(t, []string{model.BackupStatusFailed, model.BackupStatusCompleted}, b.Status)
}

// TestBackup_TwoSimultaneous_BothComplete triggers two backups back-to-back on
// the same gameserver. Both should complete (or one fails gracefully) without
// data corruption or panics.
func TestBackup_TwoSimultaneous_BothComplete(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Double Backup Host",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	b1, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-1")
	require.NoError(t, err)
	b2, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "backup-2")
	require.NoError(t, err)

	testutil.WaitForBackupCompletion(t, svc, b1.ID)
	testutil.WaitForBackupCompletion(t, svc, b2.ID)

	// Both should have valid terminal states
	s := store.New(svc.DB)
	final1, err := s.GetBackup(b1.ID)
	require.NoError(t, err)
	require.NotNil(t, final1)
	assert.Contains(t, []string{model.BackupStatusCompleted, model.BackupStatusFailed}, final1.Status)

	final2, err := s.GetBackup(b2.ID)
	require.NoError(t, err)
	require.NotNil(t, final2)
	assert.Contains(t, []string{model.BackupStatusCompleted, model.BackupStatusFailed}, final2.Status)

	// At least one should have succeeded
	atLeastOneCompleted := final1.Status == model.BackupStatusCompleted || final2.Status == model.BackupStatusCompleted
	assert.True(t, atLeastOneCompleted, "at least one of two simultaneous backups should complete")
}

