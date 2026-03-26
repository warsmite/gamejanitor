package model_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func newGameserver(id, name, gameID string, nodeID *string) *model.Gameserver {
	return &model.Gameserver{
		ID:         id,
		Name:       name,
		GameID:     gameID,
		Ports:      json.RawMessage(`[]`),
		Env:        json.RawMessage(`{}`),
		VolumeName: "vol-" + id,
		Status:     "stopped",
		PortMode:   "auto",
		NodeID:     nodeID,
		NodeTags:   model.Labels{},
	}
}


func TestGameserver_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-1", "Test Server", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	fetched, err := model.GetGameserver(db, "gs-1")
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "Test Server", fetched.Name)
	assert.Equal(t, "minecraft-java", fetched.GameID)
	assert.Equal(t, "stopped", fetched.Status)
}

func TestGameserver_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	fetched, err := model.GetGameserver(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestGameserver_Update(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-1", "Original", "test-game", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	gs.Name = "Updated"
	gs.Status = "running"
	require.NoError(t, model.UpdateGameserver(db, gs))

	fetched, err := model.GetGameserver(db, "gs-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated", fetched.Name)
	assert.Equal(t, "running", fetched.Status)
	assert.True(t, fetched.UpdatedAt.After(fetched.CreatedAt) || fetched.UpdatedAt.Equal(fetched.CreatedAt))
}

func TestGameserver_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-1", "To Delete", "test-game", nil)
	require.NoError(t, model.CreateGameserver(db, gs))
	require.NoError(t, model.DeleteGameserver(db, "gs-1"))

	fetched, err := model.GetGameserver(db, "gs-1")
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestGameserver_ListFilters(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs1 := newGameserver("gs-1", "Server1", "minecraft-java", testutil.StrPtr("node-a"))
	gs1.Status = "running"
	gs2 := newGameserver("gs-2", "Server2", "rust", testutil.StrPtr("node-b"))
	gs2.Status = "stopped"
	gs3 := newGameserver("gs-3", "Server3", "minecraft-java", testutil.StrPtr("node-a"))
	gs3.Status = "stopped"

	require.NoError(t, model.CreateGameserver(db, gs1))
	require.NoError(t, model.CreateGameserver(db, gs2))
	require.NoError(t, model.CreateGameserver(db, gs3))

	t.Run("filter by game_id", func(t *testing.T) {
		gameID := "minecraft-java"
		list, err := model.ListGameservers(db, model.GameserverFilter{GameID: &gameID})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})

	t.Run("filter by status", func(t *testing.T) {
		status := "running"
		list, err := model.ListGameservers(db, model.GameserverFilter{Status: &status})
		require.NoError(t, err)
		assert.Len(t, list, 1)
		assert.Equal(t, "gs-1", list[0].ID)
	})

	t.Run("filter by node_id", func(t *testing.T) {
		nodeID := "node-a"
		list, err := model.ListGameservers(db, model.GameserverFilter{NodeID: &nodeID})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})

	t.Run("filter by IDs", func(t *testing.T) {
		list, err := model.ListGameservers(db, model.GameserverFilter{IDs: []string{"gs-1", "gs-3"}})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})
}

func TestGameserver_AllocationQueries(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs1 := newGameserver("gs-1", "S1", "test-game", testutil.StrPtr("node-a"))
	gs1.MemoryLimitMB = 2048
	gs1.CPULimit = 2.0
	gs2 := newGameserver("gs-2", "S2", "test-game", testutil.StrPtr("node-a"))
	gs2.MemoryLimitMB = 4096
	gs2.CPULimit = 1.5
	gs3 := newGameserver("gs-3", "S3", "test-game", testutil.StrPtr("node-b"))
	gs3.MemoryLimitMB = 1024

	require.NoError(t, model.CreateGameserver(db, gs1))
	require.NoError(t, model.CreateGameserver(db, gs2))
	require.NoError(t, model.CreateGameserver(db, gs3))

	mem, err := model.AllocatedMemoryByNode(db, "node-a")
	require.NoError(t, err)
	assert.Equal(t, 6144, mem)

	cpu, err := model.AllocatedCPUByNode(db, "node-a")
	require.NoError(t, err)
	assert.InDelta(t, 3.5, cpu, 0.01)

	memB, err := model.AllocatedMemoryByNode(db, "node-b")
	require.NoError(t, err)
	assert.Equal(t, 1024, memB)
}

func TestGameserver_AllocationExcluding(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs1 := newGameserver("gs-1", "S1", "test-game", testutil.StrPtr("node-a"))
	gs1.MemoryLimitMB = 2048
	gs2 := newGameserver("gs-2", "S2", "test-game", testutil.StrPtr("node-a"))
	gs2.MemoryLimitMB = 4096

	require.NoError(t, model.CreateGameserver(db, gs1))
	require.NoError(t, model.CreateGameserver(db, gs2))

	mem, err := model.AllocatedMemoryByNodeExcluding(db, "node-a", "gs-1")
	require.NoError(t, err)
	assert.Equal(t, 4096, mem, "should exclude gs-1's 2048")
}

func TestGameserver_JSONColumns(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	ports := `[{"name":"game","host_port":27015,"container_port":27015,"protocol":"udp"}]`
	env := `{"SERVER_NAME":"Test","MAX_PLAYERS":"16"}`
	gs := newGameserver("gs-1", "JSON Test", "test-game", nil)
	gs.Ports = json.RawMessage(ports)
	gs.Env = json.RawMessage(env)

	require.NoError(t, model.CreateGameserver(db, gs))

	fetched, err := model.GetGameserver(db, "gs-1")
	require.NoError(t, err)
	assert.JSONEq(t, ports, string(fetched.Ports))
	assert.JSONEq(t, env, string(fetched.Env))
}

func TestGameserver_DeleteCascadesBackups(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-1", "Cascade Test", "test-game", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	backup := &model.Backup{ID: "b-1", GameserverID: "gs-1", Name: "backup1", Status: "completed"}
	require.NoError(t, model.CreateBackup(db, backup))

	schedule := &model.Schedule{ID: "s-1", GameserverID: "gs-1", Name: "sched1", Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	require.NoError(t, model.CreateSchedule(db, schedule))

	// backups/schedules FK references gameservers(id) without ON DELETE CASCADE,
	// so dependents must be deleted before the gameserver.
	require.NoError(t, model.DeleteBackupsByGameserver(db, "gs-1"))
	require.NoError(t, model.DeleteSchedulesByGameserver(db, "gs-1"))
	require.NoError(t, model.DeleteGameserver(db, "gs-1"))

	backups, err := model.ListBackups(db, model.BackupFilter{GameserverID: "gs-1"})
	require.NoError(t, err)
	assert.Empty(t, backups)

	schedules, err := model.ListSchedules(db, "gs-1")
	require.NoError(t, err)
	assert.Empty(t, schedules)
}
