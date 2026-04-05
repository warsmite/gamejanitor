package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestBackup_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-bak", "Backup Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b := &model.Backup{
		ID:           "bak-1",
		GameserverID: "gs-bak",
		Name:         "world-backup",
		Status:       model.BackupStatusCompleted,
		SizeBytes:    1048576,
	}
	require.NoError(t, db.CreateBackup(b))
	assert.False(t, b.CreatedAt.IsZero())

	got, err := db.GetBackup("bak-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "bak-1", got.ID)
	assert.Equal(t, "gs-bak", got.GameserverID)
	assert.Equal(t, "world-backup", got.Name)
	assert.Equal(t, int64(1048576), got.SizeBytes)
	// Status defaults to completed when no activity exists
	assert.Equal(t, model.BackupStatusCompleted, got.Status)
	assert.Empty(t, got.ErrorReason)
}

func TestBackup_GetNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	got, err := db.GetBackup("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_ListByGameserver(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-list", "List Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	for i := 0; i < 3; i++ {
		b := &model.Backup{
			ID:           "bak-" + string(rune('a'+i)),
			GameserverID: "gs-list",
			Name:         "backup-" + string(rune('a'+i)),
		}
		require.NoError(t, db.CreateBackup(b))
	}

	list, err := db.ListBackups(model.BackupFilter{GameserverID: "gs-list"})
	require.NoError(t, err)
	assert.Len(t, list, 3)
}

func TestBackup_ListByGameserver_Empty(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	list, err := db.ListBackups(model.BackupFilter{GameserverID: "gs-nonexistent"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_PopulateStatusFromActivity(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-pop", "Populate Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b := &model.Backup{
		ID:           "bak-pop",
		GameserverID: "gs-pop",
		Name:         "status-backup",
		Status:       model.BackupStatusCompleted,
	}
	require.NoError(t, db.CreateBackup(b))

	got, err := db.GetBackup("bak-pop")
	require.NoError(t, err)
	assert.Equal(t, model.BackupStatusCompleted, got.Status)

	// Update status to in_progress
	got.Status = model.BackupStatusInProgress
	require.NoError(t, db.UpdateBackup(got))

	got, err = db.GetBackup("bak-pop")
	require.NoError(t, err)
	assert.Equal(t, model.BackupStatusInProgress, got.Status)

	// Complete it
	got.Status = model.BackupStatusCompleted
	require.NoError(t, db.UpdateBackup(got))

	got, err = db.GetBackup("bak-pop")
	require.NoError(t, err)
	assert.Equal(t, model.BackupStatusCompleted, got.Status)
}

func TestBackup_PopulateStatusFailed(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-fail", "Fail Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b := &model.Backup{
		ID:           "bak-fail",
		GameserverID: "gs-fail",
		Name:         "failing-backup",
	}
	require.NoError(t, db.CreateBackup(b))

	got, err := db.GetBackup("bak-fail")
	require.NoError(t, err)
	require.NotNil(t, got)

	got.Status = model.BackupStatusFailed
	require.NoError(t, db.UpdateBackup(got))

	got, err = db.GetBackup("bak-fail")
	require.NoError(t, err)
	assert.Equal(t, model.BackupStatusFailed, got.Status)
}

func TestBackup_Delete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-bdel", "Del Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b := &model.Backup{
		ID:           "bak-del",
		GameserverID: "gs-bdel",
		Name:         "delete-me",
	}
	require.NoError(t, db.CreateBackup(b))

	require.NoError(t, db.DeleteBackup("bak-del"))

	got, err := db.GetBackup("bak-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestBackup_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	err := db.DeleteBackup("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestBackup_DeleteByGameserver(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-bulk", "Bulk Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	for _, id := range []string{"bak-1", "bak-2", "bak-3"} {
		b := &model.Backup{
			ID:           id,
			GameserverID: "gs-bulk",
			Name:         id,
		}
		require.NoError(t, db.CreateBackup(b))
	}

	require.NoError(t, db.DeleteBackupsByGameserver("gs-bulk"))

	list, err := db.ListBackups(model.BackupFilter{GameserverID: "gs-bulk"})
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestBackup_TotalSizeByGameserver(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-size", "Size Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b1 := &model.Backup{ID: "bak-s1", GameserverID: "gs-size", Name: "b1", SizeBytes: 100}
	b2 := &model.Backup{ID: "bak-s2", GameserverID: "gs-size", Name: "b2", SizeBytes: 250}
	require.NoError(t, db.CreateBackup(b1))
	require.NoError(t, db.CreateBackup(b2))

	total, err := db.TotalBackupSizeByGameserver("gs-size")
	require.NoError(t, err)
	assert.Equal(t, int64(350), total)

	// No backups returns 0
	totalEmpty, err := db.TotalBackupSizeByGameserver("gs-nonexistent")
	require.NoError(t, err)
	assert.Equal(t, int64(0), totalEmpty)
}

func TestBackup_CascadeOnGameserverDelete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-cas", "Cascade Host", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	b := &model.Backup{
		ID:           "bak-cas",
		GameserverID: "gs-cas",
		Name:         "cascade-backup",
	}
	require.NoError(t, db.CreateBackup(b))

	// Must delete backups before gameserver (no ON DELETE CASCADE on this FK).
	require.NoError(t, db.DeleteBackupsByGameserver("gs-cas"))
	require.NoError(t, db.DeleteGameserver("gs-cas"))

	got, err := db.GetBackup("bak-cas")
	require.NoError(t, err)
	assert.Nil(t, got)
}
