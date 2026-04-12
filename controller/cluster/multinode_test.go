package cluster_test

import (
	"github.com/warsmite/gamejanitor/controller/cluster"
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/worker"
)

// --- Placement tests ---

func TestPlacement_PrefersWorkerWithMostHeadroom(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	// Worker A: 16GB limit, 12GB already allocated
	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-busy", Name: "busy"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-busy", intPtr(16000), nil, nil))
	autoRestart := false
	gs := &model.Gameserver{ID: "gs-heavy", Name: "Heavy", GameID: "test", MemoryLimitMB: 12000, AutoRestart: &autoRestart}
	gs.NodeID = strPtr("w-busy")
	gs.VolumeName = "vol-heavy"
	require.NoError(t, s.CreateGameserver(gs))

	// Worker B: 16GB limit, 2GB allocated
	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-free", Name: "free"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-free", intPtr(16000), nil, nil))
	gs2 := &model.Gameserver{ID: "gs-light", Name: "Light", GameID: "test", MemoryLimitMB: 2000, AutoRestart: &autoRestart}
	gs2.NodeID = strPtr("w-free")
	gs2.VolumeName = "vol-light"
	require.NoError(t, s.CreateGameserver(gs2))

	reg := cluster.NewRegistry(s, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)
	reg.Register("w-busy", fw1, cluster.WorkerInfo{ID: "w-busy"})
	reg.Register("w-free", fw2, cluster.WorkerInfo{ID: "w-free"})

	dispatcher := cluster.NewDispatcher(reg, s, log)
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{})

	require.Len(t, candidates, 2)
	assert.Equal(t, "w-free", candidates[0].NodeID, "worker with more headroom should rank first")
}

func TestPlacement_SkipsCordoned(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-active", Name: "active"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-active", intPtr(16000), nil, nil))

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-cordoned", Name: "cordoned"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-cordoned", intPtr(16000), nil, nil))
	require.NoError(t, s.SetWorkerNodeCordoned("w-cordoned", true))

	reg := cluster.NewRegistry(s, log)
	reg.Register("w-active", testutil.NewFakeWorker(t), cluster.WorkerInfo{ID: "w-active"})
	reg.Register("w-cordoned", testutil.NewFakeWorker(t), cluster.WorkerInfo{ID: "w-cordoned"})

	dispatcher := cluster.NewDispatcher(reg, s, log)
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{})

	require.Len(t, candidates, 1)
	assert.Equal(t, "w-active", candidates[0].NodeID)
}

func TestPlacement_LabelFiltering(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-gpu", Name: "gpu"}))
	require.NoError(t, s.SetWorkerNodeTags("w-gpu", model.Labels{"gpu": "true"}))

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-plain", Name: "plain"}))

	reg := cluster.NewRegistry(s, log)
	reg.Register("w-gpu", testutil.NewFakeWorker(t), cluster.WorkerInfo{ID: "w-gpu"})
	reg.Register("w-plain", testutil.NewFakeWorker(t), cluster.WorkerInfo{ID: "w-plain"})

	dispatcher := cluster.NewDispatcher(reg, s, log)

	// With label requirement
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{"gpu": "true"})
	require.Len(t, candidates, 1)
	assert.Equal(t, "w-gpu", candidates[0].NodeID)

	// Without label requirement
	candidates = dispatcher.RankWorkersForPlacement(model.Labels{})
	assert.Len(t, candidates, 2)
}

// --- Worker lifecycle integration ---

func TestWorkerLifecycle_FullCycle(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-1", Name: "worker-1"}))

	reg := cluster.NewRegistry(s, log)
	require.NoError(t, reg.LoadFromDB())

	var onlineCalls, offlineCalls []string
	reg.SetCallbacks(
		func(id string, _ worker.Worker) { onlineCalls = append(onlineCalls, id) },
		func(id string) { offlineCalls = append(offlineCalls, id) },
	)

	// 1. Loaded as offline
	info, ok := reg.GetInfo("w-1")
	require.True(t, ok)
	assert.Equal(t, model.WorkerStatusOffline, info.Status)

	// 2. Register (simulates first heartbeat dial-back)
	fw := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw, cluster.WorkerInfo{ID: "w-1", MemoryTotalMB: 8000})
	assert.Equal(t, []string{"w-1"}, onlineCalls)

	// 3. Heartbeat updates stats
	require.NoError(t, reg.UpdateHeartbeat("w-1", cluster.WorkerInfo{
		ID: "w-1", MemoryTotalMB: 8000, MemoryAvailableMB: 5000,
	}))
	info, _ = reg.GetInfo("w-1")
	assert.Equal(t, int64(5000), info.MemoryAvailableMB)

	// 4. Worker goes offline
	reg.SetOffline("w-1")
	assert.Equal(t, []string{"w-1"}, offlineCalls)
	_, ok = reg.Get("w-1")
	assert.False(t, ok)

	// 5. Worker reconnects
	fw2 := testutil.NewFakeWorker(t)
	reg.Register("w-1", fw2, cluster.WorkerInfo{ID: "w-1", MemoryTotalMB: 8000})
	assert.Equal(t, []string{"w-1", "w-1"}, onlineCalls) // called twice

	// 6. Heartbeat on new connection works
	require.NoError(t, reg.UpdateHeartbeat("w-1", cluster.WorkerInfo{
		ID: "w-1", MemoryTotalMB: 8000, MemoryAvailableMB: 6000,
	}))
	info, _ = reg.GetInfo("w-1")
	assert.Equal(t, int64(6000), info.MemoryAvailableMB)
}

// --- Worker disconnect affects gameservers ---

func TestWorkerDisconnect_GameserverBecomesUnreachable(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Disconnect Test",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Worker goes offline — dispatched operations should fail
	svc.Registry.SetOffline("worker-1")

	w := svc.Dispatcher.WorkerFor(gs.ID)
	assert.Nil(t, w, "WorkerFor should return nil for offline worker")
}

// --- Capacity enforcement ---

func TestCapacity_CreateFailsWhenWorkerFull(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	// Worker with only 1024MB capacity
	testutil.RegisterFakeWorker(t, svc, "worker-small", testutil.WithMaxMemoryMB(1024))
	ctx := testutil.TestContext()

	// First gameserver uses 512MB — should succeed
	gs1 := &model.Gameserver{
		Name:          "GS1",
		GameID:        testutil.TestGameID,
		Env:           model.Env{"REQUIRED_VAR": "v"},
		MemoryLimitMB: 512,
	}
	_, err := svc.Manager.Create(ctx, gs1)
	require.NoError(t, err)

	// Second gameserver needs 600MB — total would be 1112MB > 1024MB limit
	gs2 := &model.Gameserver{
		Name:          "GS2",
		GameID:        testutil.TestGameID,
		Env:           model.Env{"REQUIRED_VAR": "v"},
		MemoryLimitMB: 600,
	}
	_, err = svc.Manager.Create(ctx, gs2)
	assert.Error(t, err, "should fail when worker memory limit exceeded")
	assert.Contains(t, err.Error(), "memory limit")
}

// --- Concurrent creates ---

func TestConcurrentCreates_NoPortConflict(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	var wg sync.WaitGroup
	var mu sync.Mutex
	errs := make([]error, 5)
	gameservers := make([]*model.Gameserver, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gs := &model.Gameserver{
				Name:   "Concurrent-" + string(rune('A'+idx)),
				GameID: testutil.TestGameID,
				Env:    model.Env{"REQUIRED_VAR": "v"},
			}
			_, err := svc.Manager.Create(ctx, gs)
			mu.Lock()
			errs[idx] = err
			if err == nil {
				gameservers[idx] = gs
			}
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// All should succeed
	for i, err := range errs {
		require.NoError(t, err, "create %d failed", i)
	}

	// All should have unique ports
	portSet := make(map[int]bool)
	for _, gs := range gameservers {
		require.NotNil(t, gs)
		for _, p := range gs.Ports {
			hp := int(p.HostPort)
			assert.False(t, portSet[hp], "port %d allocated twice", hp)
			portSet[hp] = true
		}
	}
}

// --- Port range per worker ---

func TestPortRange_WorkerSpecificRange(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Register worker with specific port range
	fw := testutil.RegisterFakeWorker(t, svc, "worker-ranged")
	_ = fw
	s := store.New(svc.DB)
	require.NoError(t, s.SetWorkerNodePortRange("worker-ranged", intPtr(30000), intPtr(30100)))

	ctx := testutil.TestContext()
	gs := &model.Gameserver{
		Name:   "Ranged Port GS",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Ports should be within the worker's range
	loaded, err := s.GetGameserver(gs.ID)
	require.NoError(t, err)
	for _, p := range loaded.Ports {
		hp := int(p.HostPort)
		assert.GreaterOrEqual(t, hp, 30000, "port should be >= range start")
		assert.LessOrEqual(t, hp, 30100, "port should be <= range end")
	}
}

// --- Port range overlap validation ---

func TestPortRange_OverlapRejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-a")
	testutil.RegisterFakeWorker(t, svc, "worker-b")

	s := store.New(svc.DB)
	log := testutil.TestLogger()
	wnSvc := cluster.NewWorkerNodeService(s, svc.Registry, svc.Broadcaster, log)

	ctx := context.Background()

	// Set range for A
	require.NoError(t, wnSvc.Update(ctx, "worker-a", &cluster.WorkerNodeUpdate{
		PortRangeStart: intPtr(25000),
		PortRangeEnd:   intPtr(25100),
	}))

	// Set overlapping range for B — should fail
	err := wnSvc.Update(ctx, "worker-b", &cluster.WorkerNodeUpdate{
		PortRangeStart: intPtr(25050),
		PortRangeEnd:   intPtr(25150),
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "overlaps")
}

// --- Helpers ---

func strPtr(s string) *string { return &s }
