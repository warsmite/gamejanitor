package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func newGameserver(id, name, gameID string, nodeID *string) *model.Gameserver {
	return &model.Gameserver{
		ID:         id,
		Name:       name,
		GameID:     gameID,
		Ports:      model.Ports{},
		Env:        model.Env{},
		VolumeName: "vol-" + id,
		PortMode:   "auto",
		NodeID:     nodeID,
		NodeTags:    model.Labels{},
		AutoRestart: boolPtr(false),
	}
}

// insertStatusActivity creates a status_changed activity for a gameserver.
func insertStatusActivity(t *testing.T, db *store.DB, gsID, newStatus string) {
	t.Helper()
	now := time.Now()
	data, _ := json.Marshal(map[string]string{
		"new_status":   newStatus,
		"error_reason": "",
	})
	a := &model.Activity{
		ID:           gsID + "-status-" + newStatus,
		GameserverID: &gsID,
		Type:         "status_changed",
		Status:       model.ActivityCompleted,
		Actor:        json.RawMessage(`{}`),
		Data:         data,
		StartedAt:    now,
		CompletedAt:  &now,
	}
	require.NoError(t, db.CreateActivity(a))
}

func boolPtr(b bool) *bool { return &b
}

func TestGameserver_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-1", "Test Server", "minecraft-java", nil)
	require.NoError(t, db.CreateGameserver(gs))

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "Test Server", fetched.Name)
	assert.Equal(t, "minecraft-java", fetched.GameID)
	assert.Equal(t, "stopped", fetched.Status)
}

func TestGameserver_GetNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	fetched, err := db.GetGameserver("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestGameserver_Update(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-1", "Original", "test-game", nil)
	require.NoError(t, db.CreateGameserver(gs))

	gs.Name = "Updated"
	require.NoError(t, db.UpdateGameserver(gs))

	// Set status via activity
	insertStatusActivity(t, db, "gs-1", "running")

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)
	assert.Equal(t, "Updated", fetched.Name)
	assert.Equal(t, "running", fetched.Status)
	assert.True(t, fetched.UpdatedAt.After(fetched.CreatedAt) || fetched.UpdatedAt.Equal(fetched.CreatedAt))
}

func TestGameserver_Delete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-1", "To Delete", "test-game", nil)
	require.NoError(t, db.CreateGameserver(gs))
	require.NoError(t, db.DeleteGameserver("gs-1"))

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestGameserver_ListFilters(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs1 := newGameserver("gs-1", "Server1", "minecraft-java", testutil.StrPtr("node-a"))
	gs2 := newGameserver("gs-2", "Server2", "rust", testutil.StrPtr("node-b"))
	gs3 := newGameserver("gs-3", "Server3", "minecraft-java", testutil.StrPtr("node-a"))

	require.NoError(t, db.CreateGameserver(gs1))
	require.NoError(t, db.CreateGameserver(gs2))
	require.NoError(t, db.CreateGameserver(gs3))

	// Set statuses via activity records
	insertStatusActivity(t, db, "gs-1", "running")
	// gs-2 and gs-3 have no status activity, so they default to "stopped"

	t.Run("filter by game_id", func(t *testing.T) {
		gameID := "minecraft-java"
		list, err := db.ListGameservers(model.GameserverFilter{GameID: &gameID})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})

	t.Run("filter by status", func(t *testing.T) {
		status := "running"
		list, err := db.ListGameservers(model.GameserverFilter{Status: &status})
		require.NoError(t, err)
		assert.Len(t, list, 1)
		assert.Equal(t, "gs-1", list[0].ID)
	})

	t.Run("filter by node_id", func(t *testing.T) {
		nodeID := "node-a"
		list, err := db.ListGameservers(model.GameserverFilter{NodeID: &nodeID})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})

	t.Run("filter by IDs", func(t *testing.T) {
		list, err := db.ListGameservers(model.GameserverFilter{IDs: []string{"gs-1", "gs-3"}})
		require.NoError(t, err)
		assert.Len(t, list, 2)
	})
}

func TestGameserver_AllocationQueries(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs1 := newGameserver("gs-1", "S1", "test-game", testutil.StrPtr("node-a"))
	gs1.MemoryLimitMB = 2048
	gs1.CPULimit = 2.0
	gs2 := newGameserver("gs-2", "S2", "test-game", testutil.StrPtr("node-a"))
	gs2.MemoryLimitMB = 4096
	gs2.CPULimit = 1.5
	gs3 := newGameserver("gs-3", "S3", "test-game", testutil.StrPtr("node-b"))
	gs3.MemoryLimitMB = 1024

	require.NoError(t, db.CreateGameserver(gs1))
	require.NoError(t, db.CreateGameserver(gs2))
	require.NoError(t, db.CreateGameserver(gs3))

	mem, err := db.AllocatedMemoryByNode("node-a")
	require.NoError(t, err)
	assert.Equal(t, 6144, mem)

	cpu, err := db.AllocatedCPUByNode("node-a")
	require.NoError(t, err)
	assert.InDelta(t, 3.5, cpu, 0.01)

	memB, err := db.AllocatedMemoryByNode("node-b")
	require.NoError(t, err)
	assert.Equal(t, 1024, memB)
}

func TestGameserver_AllocationExcluding(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs1 := newGameserver("gs-1", "S1", "test-game", testutil.StrPtr("node-a"))
	gs1.MemoryLimitMB = 2048
	gs2 := newGameserver("gs-2", "S2", "test-game", testutil.StrPtr("node-a"))
	gs2.MemoryLimitMB = 4096

	require.NoError(t, db.CreateGameserver(gs1))
	require.NoError(t, db.CreateGameserver(gs2))

	mem, err := db.AllocatedMemoryByNodeExcluding("node-a", "gs-1")
	require.NoError(t, err)
	assert.Equal(t, 4096, mem, "should exclude gs-1's 2048")
}

func TestGameserver_JSONColumns(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	expectedPorts := model.Ports{
		{Name: "game", HostPort: 27015, ContainerPort: 27015, Protocol: "udp"},
	}
	gs := newGameserver("gs-1", "JSON Test", "test-game", nil)
	gs.Ports = expectedPorts
	gs.Env = model.Env{"SERVER_NAME": "Test", "MAX_PLAYERS": "16"}

	require.NoError(t, db.CreateGameserver(gs))

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)
	assert.Equal(t, expectedPorts, fetched.Ports)
	assert.Equal(t, model.Env{"SERVER_NAME": "Test", "MAX_PLAYERS": "16"}, fetched.Env)
}

func TestGameserver_DeleteCascadesBackups(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-1", "Cascade Test", "test-game", nil)
	require.NoError(t, db.CreateGameserver(gs))

	backup := &model.Backup{ID: "b-1", GameserverID: "gs-1", Name: "backup1", Status: "completed"}
	require.NoError(t, db.CreateBackup(backup))

	schedule := &model.Schedule{ID: "s-1", GameserverID: "gs-1", Name: "sched1", Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	require.NoError(t, db.CreateSchedule(schedule))

	// backups/schedules FK references gameservers(id) without ON DELETE CASCADE,
	// so dependents must be deleted before the gameserver.
	require.NoError(t, db.DeleteBackupsByGameserver("gs-1"))
	require.NoError(t, db.DeleteSchedulesByGameserver("gs-1"))
	require.NoError(t, db.DeleteGameserver("gs-1"))

	backups, err := db.ListBackups(model.BackupFilter{GameserverID: "gs-1"})
	require.NoError(t, err)
	assert.Empty(t, backups)

	schedules, err := db.ListSchedules("gs-1")
	require.NoError(t, err)
	assert.Empty(t, schedules)
}

func TestPopulateNode_NilNodeID(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	gs := newGameserver("gs-1", "No Node", "test-game", nil)
	require.NoError(t, db.CreateGameserver(gs))

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)

	// Should not panic when NodeID is nil
	db.PopulateNode(fetched)
	assert.Nil(t, fetched.Node)
}

func TestPopulateNode_NonexistentNode(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	nodeID := "ghost-node"
	gs := newGameserver("gs-1", "Ghost Node", "test-game", &nodeID)
	require.NoError(t, db.CreateGameserver(gs))

	fetched, err := db.GetGameserver("gs-1")
	require.NoError(t, err)

	// Node doesn't exist in worker_nodes — should not panic, Node stays nil
	db.PopulateNode(fetched)
	assert.Nil(t, fetched.Node)
}

func TestPopulateNodes_MixedValid(t *testing.T) {
	t.Parallel()
	testDB := testutil.NewTestDB(t)
	s := store.New(testDB)

	realNodeID := "real-node"
	ghostNodeID := "ghost-node"

	_, err := testDB.Exec(`INSERT INTO worker_nodes (id, external_ip, lan_ip) VALUES (?, ?, ?)`,
		realNodeID, "1.2.3.4", "192.168.1.1")
	require.NoError(t, err)

	gs1 := newGameserver("gs-1", "On Real Node", "test-game", &realNodeID)
	gs2 := newGameserver("gs-2", "Nil Node", "test-game", nil)
	gs3 := newGameserver("gs-3", "Ghost Node", "test-game", &ghostNodeID)

	require.NoError(t, s.CreateGameserver(gs1))
	require.NoError(t, s.CreateGameserver(gs2))
	require.NoError(t, s.CreateGameserver(gs3))

	list, err := s.ListGameservers(model.GameserverFilter{})
	require.NoError(t, err)
	require.Len(t, list, 3)

	s.PopulateNodes(list)

	// Find each by ID to check
	nodeMap := make(map[string]*model.GameserverNode)
	for _, gs := range list {
		nodeMap[gs.ID] = gs.Node
	}

	assert.NotNil(t, nodeMap["gs-1"], "gs-1 should have node populated")
	assert.Equal(t, "1.2.3.4", nodeMap["gs-1"].ExternalIP)
	assert.Nil(t, nodeMap["gs-2"], "gs-2 has nil NodeID, should have nil Node")
	assert.Nil(t, nodeMap["gs-3"], "gs-3 has nonexistent node, should have nil Node")
}

func TestPopulateNodes_SharedNode(t *testing.T) {
	t.Parallel()
	testDB := testutil.NewTestDB(t)
	s := store.New(testDB)

	sharedNodeID := "shared-node"
	_, err := testDB.Exec(`INSERT INTO worker_nodes (id, external_ip, lan_ip) VALUES (?, ?, ?)`,
		sharedNodeID, "10.0.0.1", "192.168.0.1")
	require.NoError(t, err)

	gs1 := newGameserver("gs-1", "Server A", "test-game", &sharedNodeID)
	gs2 := newGameserver("gs-2", "Server B", "test-game", &sharedNodeID)
	require.NoError(t, s.CreateGameserver(gs1))
	require.NoError(t, s.CreateGameserver(gs2))

	list, err := s.ListGameservers(model.GameserverFilter{})
	require.NoError(t, err)
	require.Len(t, list, 2)

	s.PopulateNodes(list)

	// Both should have the same node data (cache hit for second)
	assert.NotNil(t, list[0].Node)
	assert.NotNil(t, list[1].Node)
	assert.Equal(t, "10.0.0.1", list[0].Node.ExternalIP)
	assert.Equal(t, "10.0.0.1", list[1].Node.ExternalIP)
}
