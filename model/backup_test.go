package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestBackup_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bak", "Backup Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b := &model.Backup{
		ID:           "bak-1",
		GameserverID: "gs-bak",
		Name:         "world-backup",
		SizeBytes:    1048576,
		Status:       model.BackupStatusCompleted,
	}
	require.NoError(t, model.CreateBackup(db, b))
	assert.False(t, b.CreatedAt.IsZero())

	got, err := model.GetBackup(db, "bak-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "bak-1", got.ID)
	assert.Equal(t, "gs-bak", got.GameserverID)
	assert.Equal(t, "world-backup", got.Name)
	assert.Equal(t, int64(1048576), got.SizeBytes)
	assert.Equal(t, model.BackupStatusCompleted, got.Status)
	assert.Empty(t, got.ErrorReason)
}

func TestBackup_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	got, err := model.GetBackup(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_ListByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-list", "List Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	for i := 0; i < 3; i++ {
		b := &model.Backup{
			ID:           "bak-" + string(rune('a'+i)),
			GameserverID: "gs-list",
			Name:         "backup-" + string(rune('a'+i)),
			Status:       model.BackupStatusCompleted,
		}
		require.NoError(t, model.CreateBackup(db, b))
	}

	list, err := model.ListBackups(db, model.BackupFilter{GameserverID: "gs-list"})
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestBackup_ListByGameserver_Empty(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	list, err := model.ListBackups(db, model.BackupFilter{GameserverID: "gs-nonexistent"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_UpdateStatus(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-upd", "Update Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b := &model.Backup{
		ID:           "bak-upd",
		GameserverID: "gs-upd",
		Name:         "updating-backup",
		Status:       model.BackupStatusInProgress,
	}
	require.NoError(t, model.CreateBackup(db, b))

	require.NoError(t, model.UpdateBackupStatus(db, "bak-upd", model.BackupStatusCompleted, 2097152, ""))

	got, err := model.GetBackup(db, "bak-upd")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.BackupStatusCompleted, got.Status)
	assert.Equal(t, int64(2097152), got.SizeBytes)
	assert.Empty(t, got.ErrorReason)
}

func TestBackup_UpdateStatusFailed(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-fail", "Fail Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b := &model.Backup{
		ID:           "bak-fail",
		GameserverID: "gs-fail",
		Name:         "failing-backup",
		Status:       model.BackupStatusInProgress,
	}
	require.NoError(t, model.CreateBackup(db, b))

	require.NoError(t, model.UpdateBackupStatus(db, "bak-fail", model.BackupStatusFailed, 0, "disk full"))

	got, err := model.GetBackup(db, "bak-fail")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, model.BackupStatusFailed, got.Status)
	assert.Equal(t, "disk full", got.ErrorReason)
}

func TestBackup_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bdel", "Del Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b := &model.Backup{
		ID:           "bak-del",
		GameserverID: "gs-bdel",
		Name:         "delete-me",
		Status:       model.BackupStatusCompleted,
	}
	require.NoError(t, model.CreateBackup(db, b))

	require.NoError(t, model.DeleteBackup(db, "bak-del"))

	got, err := model.GetBackup(db, "bak-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	err := model.DeleteBackup(db, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestBackup_DeleteByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bulk", "Bulk Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	for _, id := range []string{"bak-1", "bak-2", "bak-3"} {
		b := &model.Backup{
			ID:           id,
			GameserverID: "gs-bulk",
			Name:         id,
			Status:       model.BackupStatusCompleted,
		}
		require.NoError(t, model.CreateBackup(db, b))
	}

	require.NoError(t, model.DeleteBackupsByGameserver(db, "gs-bulk"))

	list, err := model.ListBackups(db, model.BackupFilter{GameserverID: "gs-bulk"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_TotalSizeByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-size", "Size Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b1 := &model.Backup{ID: "bak-s1", GameserverID: "gs-size", Name: "b1", SizeBytes: 100, Status: model.BackupStatusCompleted}
	b2 := &model.Backup{ID: "bak-s2", GameserverID: "gs-size", Name: "b2", SizeBytes: 250, Status: model.BackupStatusCompleted}
	require.NoError(t, model.CreateBackup(db, b1))
	require.NoError(t, model.CreateBackup(db, b2))

	total, err := model.TotalBackupSizeByGameserver(db, "gs-size")
	require.NoError(t, err)
	assert.Equal(t, int64(350), total)

	// No backups returns 0
	totalEmpty, err := model.TotalBackupSizeByGameserver(db, "gs-nonexistent")
	require.NoError(t, err)
	assert.Equal(t, int64(0), totalEmpty)
}

func TestBackup_CascadeOnGameserverDelete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-cas", "Cascade Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	b := &model.Backup{
		ID:           "bak-cas",
		GameserverID: "gs-cas",
		Name:         "cascade-backup",
		Status:       model.BackupStatusCompleted,
	}
	require.NoError(t, model.CreateBackup(db, b))

	// Must delete backups before gameserver (no ON DELETE CASCADE on this FK).
	require.NoError(t, model.DeleteBackupsByGameserver(db, "gs-cas"))
	require.NoError(t, model.DeleteGameserver(db, "gs-cas"))

	got, err := model.GetBackup(db, "bak-cas")
	require.NoError(t, err)
	assert.Nil(t, got)
}

