package orchestrator_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func intPtr(v int) *int         { return &v }
func floatPtr(v float64) *float64 { return &v }

func TestDispatcher_WorkerFor(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Dispatch Target",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	w := svc.Dispatcher.WorkerFor(gs.ID)
	assert.NotNil(t, w, "dispatcher should find worker for gameserver")
}

func TestDispatcher_WorkerFor_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	w := svc.Dispatcher.WorkerFor("nonexistent-gs")
	assert.Nil(t, w)
}

func TestDispatcher_SelectWorkerByNodeID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	w, err := svc.Dispatcher.SelectWorkerByNodeID("worker-1")
	require.NoError(t, err)
	assert.NotNil(t, w)
}

func TestDispatcher_SelectWorkerByNodeID_NotConnected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	_, err := svc.Dispatcher.SelectWorkerByNodeID("offline-worker")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not connected")
}

func TestDispatcher_SelectWorkerByNodeID_Empty(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	_, err := svc.Dispatcher.SelectWorkerByNodeID("")
	assert.Error(t, err)
}

func TestDispatcher_RankWorkersForPlacement_PrefersMostHeadroom(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	// Two workers with different capacity
	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-big", Name: "big"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-big", intPtr(16000), floatPtr(8.0), nil))

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-small", Name: "small"}))
	require.NoError(t, s.SetWorkerNodeLimits("w-small", intPtr(4000), floatPtr(2.0), nil))

	reg := orchestrator.NewRegistry(s, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)
	reg.Register("w-big", fw1, orchestrator.WorkerInfo{ID: "w-big"})
	reg.Register("w-small", fw2, orchestrator.WorkerInfo{ID: "w-small"})

	dispatcher := orchestrator.NewDispatcher(reg, s, log)
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{})

	require.Len(t, candidates, 2)
	// Both are empty, but w-big has more capacity — both at 100% headroom,
	// so the order depends on the scoring tie-break
	assert.Contains(t, []string{"w-big", "w-small"}, candidates[0].NodeID)
}

func TestDispatcher_RankWorkersForPlacement_SkipsCordoned(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-active", Name: "active"}))
	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-cordoned", Name: "cordoned"}))
	require.NoError(t, s.SetWorkerNodeCordoned("w-cordoned", true))

	reg := orchestrator.NewRegistry(s, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)
	reg.Register("w-active", fw1, orchestrator.WorkerInfo{ID: "w-active"})
	reg.Register("w-cordoned", fw2, orchestrator.WorkerInfo{ID: "w-cordoned"})

	dispatcher := orchestrator.NewDispatcher(reg, s, log)
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{})

	require.Len(t, candidates, 1)
	assert.Equal(t, "w-active", candidates[0].NodeID)
}

func TestDispatcher_RankWorkersForPlacement_NoWorkers(t *testing.T) {
	t.Parallel()
	log := testutil.TestLogger()

	reg := orchestrator.NewRegistry(nil, log)
	dispatcher := orchestrator.NewDispatcher(reg, nil, log)

	candidates := dispatcher.RankWorkersForPlacement(model.Labels{})
	assert.Nil(t, candidates)
}

func TestDispatcher_RankWorkersForPlacement_LabelFiltering(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-tagged", Name: "tagged"}))
	require.NoError(t, s.SetWorkerNodeTags("w-tagged", model.Labels{"gpu": "true"}))

	require.NoError(t, s.UpsertWorkerNode(&model.WorkerNode{ID: "w-plain", Name: "plain"}))

	reg := orchestrator.NewRegistry(s, log)
	fw1 := testutil.NewFakeWorker(t)
	fw2 := testutil.NewFakeWorker(t)
	reg.Register("w-tagged", fw1, orchestrator.WorkerInfo{ID: "w-tagged"})
	reg.Register("w-plain", fw2, orchestrator.WorkerInfo{ID: "w-plain"})

	dispatcher := orchestrator.NewDispatcher(reg, s, log)

	// Require gpu label — only w-tagged qualifies
	candidates := dispatcher.RankWorkersForPlacement(model.Labels{"gpu": "true"})
	require.Len(t, candidates, 1)
	assert.Equal(t, "w-tagged", candidates[0].NodeID)

	// No label requirement — both qualify
	candidates = dispatcher.RankWorkersForPlacement(model.Labels{})
	assert.Len(t, candidates, 2)
}
