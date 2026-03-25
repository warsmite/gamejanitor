package models_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestBackup_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bak", "Backup Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b := &models.Backup{
		ID:           "bak-1",
		GameserverID: "gs-bak",
		Name:         "world-backup",
		SizeBytes:    1048576,
		Status:       models.BackupStatusCompleted,
	}
	require.NoError(t, models.CreateBackup(db, b))
	assert.False(t, b.CreatedAt.IsZero())

	got, err := models.GetBackup(db, "bak-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "bak-1", got.ID)
	assert.Equal(t, "gs-bak", got.GameserverID)
	assert.Equal(t, "world-backup", got.Name)
	assert.Equal(t, int64(1048576), got.SizeBytes)
	assert.Equal(t, models.BackupStatusCompleted, got.Status)
	assert.Empty(t, got.ErrorReason)
}

func TestBackup_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	got, err := models.GetBackup(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_ListByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-list", "List Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	for i := 0; i < 3; i++ {
		b := &models.Backup{
			ID:           "bak-" + string(rune('a'+i)),
			GameserverID: "gs-list",
			Name:         "backup-" + string(rune('a'+i)),
			Status:       models.BackupStatusCompleted,
		}
		require.NoError(t, models.CreateBackup(db, b))
	}

	list, err := models.ListBackups(db, models.BackupFilter{GameserverID: "gs-list"})
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestBackup_ListByGameserver_Empty(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	list, err := models.ListBackups(db, models.BackupFilter{GameserverID: "gs-nonexistent"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_UpdateStatus(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-upd", "Update Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b := &models.Backup{
		ID:           "bak-upd",
		GameserverID: "gs-upd",
		Name:         "updating-backup",
		Status:       models.BackupStatusInProgress,
	}
	require.NoError(t, models.CreateBackup(db, b))

	require.NoError(t, models.UpdateBackupStatus(db, "bak-upd", models.BackupStatusCompleted, 2097152, ""))

	got, err := models.GetBackup(db, "bak-upd")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, models.BackupStatusCompleted, got.Status)
	assert.Equal(t, int64(2097152), got.SizeBytes)
	assert.Empty(t, got.ErrorReason)
}

func TestBackup_UpdateStatusFailed(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-fail", "Fail Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b := &models.Backup{
		ID:           "bak-fail",
		GameserverID: "gs-fail",
		Name:         "failing-backup",
		Status:       models.BackupStatusInProgress,
	}
	require.NoError(t, models.CreateBackup(db, b))

	require.NoError(t, models.UpdateBackupStatus(db, "bak-fail", models.BackupStatusFailed, 0, "disk full"))

	got, err := models.GetBackup(db, "bak-fail")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, models.BackupStatusFailed, got.Status)
	assert.Equal(t, "disk full", got.ErrorReason)
}

func TestBackup_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bdel", "Del Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b := &models.Backup{
		ID:           "bak-del",
		GameserverID: "gs-bdel",
		Name:         "delete-me",
		Status:       models.BackupStatusCompleted,
	}
	require.NoError(t, models.CreateBackup(db, b))

	require.NoError(t, models.DeleteBackup(db, "bak-del"))

	got, err := models.GetBackup(db, "bak-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	err := models.DeleteBackup(db, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestBackup_DeleteByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-bulk", "Bulk Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	for _, id := range []string{"bak-1", "bak-2", "bak-3"} {
		b := &models.Backup{
			ID:           id,
			GameserverID: "gs-bulk",
			Name:         id,
			Status:       models.BackupStatusCompleted,
		}
		require.NoError(t, models.CreateBackup(db, b))
	}

	require.NoError(t, models.DeleteBackupsByGameserver(db, "gs-bulk"))

	list, err := models.ListBackups(db, models.BackupFilter{GameserverID: "gs-bulk"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_TotalSizeByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-size", "Size Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b1 := &models.Backup{ID: "bak-s1", GameserverID: "gs-size", Name: "b1", SizeBytes: 100, Status: models.BackupStatusCompleted}
	b2 := &models.Backup{ID: "bak-s2", GameserverID: "gs-size", Name: "b2", SizeBytes: 250, Status: models.BackupStatusCompleted}
	require.NoError(t, models.CreateBackup(db, b1))
	require.NoError(t, models.CreateBackup(db, b2))

	total, err := models.TotalBackupSizeByGameserver(db, "gs-size")
	require.NoError(t, err)
	assert.Equal(t, int64(350), total)

	// No backups returns 0
	totalEmpty, err := models.TotalBackupSizeByGameserver(db, "gs-nonexistent")
	require.NoError(t, err)
	assert.Equal(t, int64(0), totalEmpty)
}

func TestBackup_CascadeOnGameserverDelete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-cas", "Cascade Host", "minecraft-java", nil)
	require.NoError(t, models.CreateGameserver(db, gs))

	b := &models.Backup{
		ID:           "bak-cas",
		GameserverID: "gs-cas",
		Name:         "cascade-backup",
		Status:       models.BackupStatusCompleted,
	}
	require.NoError(t, models.CreateBackup(db, b))

	// Must delete backups before gameserver (no ON DELETE CASCADE on this FK).
	require.NoError(t, models.DeleteBackupsByGameserver(db, "gs-cas"))
	require.NoError(t, models.DeleteGameserver(db, "gs-cas"))

	got, err := models.GetBackup(db, "bak-cas")
	require.NoError(t, err)
	assert.Nil(t, got)
}

