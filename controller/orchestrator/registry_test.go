package orchestrator_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/worker"
)

func TestRegistry_RegisterAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-1", Name: "worker-1"}))

	reg := orchestrator.NewRegistry(s, log)
	require.NoError(t, reg.LoadFromDB())

	// Worker loaded as offline
	_, ok := reg.Get("w-1")
	assert.False(t, ok, "worker should be offline after LoadFromDB")

	info, ok := reg.GetInfo("w-1")
	assert.True(t, ok)
	assert.Equal(t, model.WorkerStatusOffline, info.Status)

	// Register with a connection
	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1", LanIP: "10.0.0.1"})

	w, ok := reg.Get("w-1")
	assert.True(t, ok)
	assert.NotNil(t, w)

	info, _ = reg.GetInfo("w-1")
	assert.Equal(t, model.WorkerStatusOnline, info.Status)
	assert.Equal(t, "10.0.0.1", info.LanIP)
}

func TestRegistry_SetOffline(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-1", Name: "worker-1"}))

	reg := orchestrator.NewRegistry(s, log)
	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1"})

	_, ok := reg.Get("w-1")
	assert.True(t, ok)

	reg.SetOffline("w-1")

	// Connection gone but still in registry
	_, ok = reg.Get("w-1")
	assert.False(t, ok, "worker should not be gettable after SetOffline")

	info, ok := reg.GetInfo("w-1")
	assert.True(t, ok, "worker should still be in registry after SetOffline")
	assert.Equal(t, model.WorkerStatusOffline, info.Status)
}

func TestRegistry_Unregister(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1"})

	assert.Equal(t, 1, reg.Count())

	reg.Unregister("w-1")

	assert.Equal(t, 0, reg.Count())
	_, ok := reg.GetInfo("w-1")
	assert.False(t, ok, "worker should be gone after Unregister")
}

func TestRegistry_UpdateHeartbeat(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{
		ID:            "w-1",
		MemoryTotalMB: 8000,
		TokenID:       "tok-original",
	})

	err := reg.UpdateHeartbeat("w-1", orchestrator.WorkerInfo{
		ID:                "w-1",
		MemoryTotalMB:     8000,
		MemoryAvailableMB: 4000,
	})
	require.NoError(t, err)

	info, _ := reg.GetInfo("w-1")
	assert.Equal(t, int64(4000), info.MemoryAvailableMB)
	assert.Equal(t, "tok-original", info.TokenID, "token should be preserved across heartbeats")
}

func TestRegistry_UpdateHeartbeat_OfflineWorkerErrors(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)

	err := reg.UpdateHeartbeat("w-nonexistent", orchestrator.WorkerInfo{ID: "w-nonexistent"})
	assert.Error(t, err)
}

func TestRegistry_Callbacks(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)

	var onlineID, offlineID string
	reg.SetCallbacks(
		func(nodeID string, _ worker.Worker) { onlineID = nodeID },
		func(nodeID string) { offlineID = nodeID },
	)

	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1"})
	assert.Equal(t, "w-1", onlineID)

	reg.SetOffline("w-1")
	assert.Equal(t, "w-1", offlineID)
}

func TestRegistry_Unregister_FiresOfflineCallback(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)

	var offlineID string
	reg.SetCallbacks(nil, func(nodeID string) { offlineID = nodeID })

	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1"})

	reg.Unregister("w-1")
	assert.Equal(t, "w-1", offlineID)
}

func TestRegistry_ListWorkers(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)

	reg.Register("w-1", fw1, orchestrator.WorkerInfo{ID: "w-1"})
	reg.Register("w-2", fw2, orchestrator.WorkerInfo{ID: "w-2"})
	reg.SetOffline("w-2")

	all := reg.ListWorkers()
	assert.Len(t, all, 2)

	online := reg.ListOnlineWorkers()
	assert.Len(t, online, 1)
	assert.Equal(t, "w-1", online[0].ID)
}

func TestRegistry_ReRegister_ReplacesConnection(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)

	reg.Register("w-1", fw1, orchestrator.WorkerInfo{ID: "w-1", LanIP: "10.0.0.1"})
	reg.Register("w-1", fw2, orchestrator.WorkerInfo{ID: "w-1", LanIP: "10.0.0.2"})

	// Should still have one worker, with updated info
	assert.Equal(t, 1, reg.Count())
	info, _ := reg.GetInfo("w-1")
	assert.Equal(t, "10.0.0.2", info.LanIP)
}

func TestRegistry_LastSeen_SetOnRegisterAndHeartbeat(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	fw := testutil.NewFakeWorker(t)

	reg.Register("w-1", fw, orchestrator.WorkerInfo{ID: "w-1"})

	info, _ := reg.GetInfo("w-1")
	assert.WithinDuration(t, time.Now(), info.LastSeen, 2*time.Second)

	time.Sleep(10 * time.Millisecond)
	reg.UpdateHeartbeat("w-1", orchestrator.WorkerInfo{ID: "w-1"})

	info2, _ := reg.GetInfo("w-1")
	assert.True(t, info2.LastSeen.After(info.LastSeen), "heartbeat should update LastSeen")
}
