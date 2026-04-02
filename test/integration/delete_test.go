package integration_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestDelete_CleansUpBackupStoreFiles(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Create a backup so there's store data to clean up
	backup, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "pre-delete-backup")
	require.NoError(t, err)

	// Wait for async backup to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, _ := svc.BackupSvc.GetBackup(gs.ID, backup.ID)
		if b != nil && b.Status != "in_progress" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Delete the gameserver — should clean up backup store files
	require.NoError(t, svc.GameserverSvc.DeleteGameserver(ctx, gs.ID))
	svc.GameserverSvc.WaitForOperations()

	// Verify backup DB records are gone (cascade)
	backups, err := svc.BackupSvc.ListBackups(model.BackupFilter{GameserverID: gs.ID})
	require.NoError(t, err)
	assert.Empty(t, backups)
}

func TestDelete_CleansUpSchedules(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Create a schedule
	sched := &model.Schedule{
		GameserverID: gs.ID, Name: "test-sched", Type: "restart",
		CronExpr: "0 0 * * *", Payload: []byte(`{}`), Enabled: true,
	}
	require.NoError(t, svc.ScheduleSvc.CreateSchedule(ctx, sched))

	require.NoError(t, svc.GameserverSvc.DeleteGameserver(ctx, gs.ID))
	svc.GameserverSvc.WaitForOperations()

	schedules, err := svc.ScheduleSvc.ListSchedules(gs.ID)
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestDelete_CleansUpVolume(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)
	assert.True(t, fw.VolumeExists(gs.VolumeName))

	require.NoError(t, svc.GameserverSvc.DeleteGameserver(ctx, gs.ID))
	svc.GameserverSvc.WaitForOperations()
	assert.False(t, fw.VolumeExists(gs.VolumeName), "volume should be removed on delete")
}

func TestDelete_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	err := svc.GameserverSvc.DeleteGameserver(ctx, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestDelete_VolumeRemovalFailure_DeleteStillCompletes(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Volume removal fails but delete should still complete —
	// an orphan volume is better than an orphan DB record
	fw.FailNext("RemoveVolume", assert.AnError)

	require.NoError(t, svc.GameserverSvc.DeleteGameserver(ctx, gs.ID))
	svc.GameserverSvc.WaitForOperations()

	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	assert.Nil(t, fetched, "gameserver should be deleted even if volume removal failed")
}
