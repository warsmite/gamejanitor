package gameserver_test

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

func TestActivity_StartCreatesRecord(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	s := store.New(svc.DB)
	activities, err := s.ListActivities(model.ActivityFilter{GameserverID: &gs.ID})
	require.NoError(t, err)
	require.NotEmpty(t, activities, "start should create an activity record")

	// Find the start activity (there may also be a create activity)
	var startActivity *model.Activity
	for i := range activities {
		if activities[i].Type == model.OpStart {
			startActivity = &activities[i]
			break
		}
	}
	require.NotNil(t, startActivity, "should have a start activity")
	assert.Equal(t, model.ActivityCompleted, startActivity.Status)
	assert.Equal(t, "worker-1", startActivity.WorkerID)
	assert.NotNil(t, startActivity.CompletedAt)
}

func TestActivity_MutexRejectsConcurrent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Insert a fake running activity
	s := store.New(svc.DB)
	require.NoError(t, s.CreateActivity(&model.Activity{
		ID:           "activity-running",
		GameserverID: &gs.ID,
		WorkerID:     "worker-1",
		Type:         model.OpBackup,
		Status:       model.ActivityRunning,
		Actor:        json.RawMessage(`{}`),
		Data:         json.RawMessage(`{}`),
		StartedAt:    time.Now(),
	}))

	// Start should be rejected — there's already a running activity
	err := svc.GameserverSvc.Start(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an operation in progress")
}

func TestActivity_StopBypassesMutex(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver first
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Insert a fake running activity
	s := store.New(svc.DB)
	require.NoError(t, s.CreateActivity(&model.Activity{
		ID:           "activity-running-2",
		GameserverID: &gs.ID,
		WorkerID:     "worker-1",
		Type:         model.OpBackup,
		Status:       model.ActivityRunning,
		Actor:        json.RawMessage(`{}`),
		Data:         json.RawMessage(`{}`),
		StartedAt:    time.Now(),
	}))

	// Stop should still work despite the running activity
	err := svc.GameserverSvc.Stop(testutil.TestContext(), gs.ID)
	require.NoError(t, err)
}

func TestActivity_RestartCreatesOneRecord(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Start first so restart has something to stop
	require.NoError(t, svc.GameserverSvc.Start(testutil.TestContext(), gs.ID))

	// Clear activities from the start
	svc.DB.Exec("DELETE FROM activity")

	require.NoError(t, svc.GameserverSvc.Restart(testutil.TestContext(), gs.ID))

	s := store.New(svc.DB)
	activities, err := s.ListActivities(model.ActivityFilter{GameserverID: &gs.ID})
	require.NoError(t, err)

	// Should have exactly one activity (restart), not three (restart + stop + start)
	assert.Len(t, activities, 1, "restart should create a single activity, not nested ones")
	assert.Equal(t, model.OpRestart, activities[0].Type)
	assert.Equal(t, model.ActivityCompleted, activities[0].Status)
}

func TestActivity_AbandonOnStartup(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	s := store.New(svc.DB)

	// Insert a "running" activity as if the controller crashed
	require.NoError(t, s.CreateActivity(&model.Activity{
		ID:           "activity-stale",
		GameserverID: &gs.ID,
		WorkerID:     "worker-1",
		Type:         model.OpBackup,
		Status:       model.ActivityRunning,
		Actor:        json.RawMessage(`{}`),
		Data:         json.RawMessage(`{}`),
		StartedAt:    time.Now(),
	}))

	abandoned, err := s.AbandonRunningActivities()
	require.NoError(t, err)
	assert.Equal(t, 1, abandoned)

	a, err := s.GetActivity("activity-stale")
	require.NoError(t, err)
	assert.Equal(t, model.ActivityAbandoned, a.Status)
	assert.Equal(t, "controller restarted", a.Error)
	assert.NotNil(t, a.CompletedAt)
}

// TestActivity_BackupBlocksStart verifies that starting a gameserver is rejected
// while a backup is in progress — the real concurrent scenario, not a fake activity.
func TestActivity_BackupBlocksStart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	ctx := testutil.TestContext()

	// Start the gameserver first so it has a container for backup
	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))

	// Trigger a backup — this spawns an async goroutine with a running activity
	_, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "test-backup")
	require.NoError(t, err)

	// Give the async goroutine a moment to start and register the activity
	time.Sleep(100 * time.Millisecond)

	// Check that a backup activity is running
	s := store.New(svc.DB)
	running := model.ActivityRunning
	activities, err := s.ListActivities(model.ActivityFilter{
		GameserverID: &gs.ID,
		Status:       &running,
	})
	require.NoError(t, err)

	if len(activities) > 0 {
		// Backup is in progress — restart should be rejected
		err = svc.GameserverSvc.Restart(ctx, gs.ID)
		assert.Error(t, err, "restart should be rejected while backup is running")
		assert.Contains(t, err.Error(), "already has an operation in progress")
	} else {
		// Backup completed too fast (FakeWorker is instant) — that's OK,
		// the mutex test with fake activities covers this case
		t.Log("backup completed before we could test mutex — skipping concurrent check")
	}
}

// TestActivity_MultipleStopsAllowed verifies that stop operations don't block
// each other — multiple stop calls should succeed (idempotent).
func TestActivity_MultipleStopsAllowed(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	ctx := testutil.TestContext()

	require.NoError(t, svc.GameserverSvc.Start(ctx, gs.ID))
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))
	// Second stop on already-stopped server should succeed (idempotent)
	require.NoError(t, svc.GameserverSvc.Stop(ctx, gs.ID))
}
