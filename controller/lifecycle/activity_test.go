package lifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestActivity_StartCreatesEvent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	err := svc.Runner.Submit(gs.ID, model.OpStart, event.Actor{Type: "test"}, func(ctx context.Context, onProgress gameserver.ProgressFunc) error {
		return svc.LifecycleSvc.Start(ctx, gs.ID, onProgress)
	})
	require.NoError(t, err)
	svc.Runner.Wait()

	s := store.New(svc.DB)
	events, err := s.ListEvents(model.EventFilter{GameserverID: &gs.ID})
	require.NoError(t, err)
	require.NotEmpty(t, events, "start should create an event record")

	var startEvent *model.Event
	for i := range events {
		if events[i].Type == "gameserver.start" {
			startEvent = &events[i]
			break
		}
	}
	require.NotNil(t, startEvent, "should have a start event")
}

func TestActivity_MutexRejectsConcurrent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	// Set an operation in progress on the gameserver row
	gs.OperationType = ptrStr(model.OpBackup)
	gs.OperationID = ptrStr("fake-op")
	require.NoError(t, store.New(svc.DB).UpdateGameserver(gs))

	// Submit through the runner — should be rejected by the operation guard
	err := svc.Runner.Submit(gs.ID, model.OpStart, event.Actor{Type: "test"}, func(ctx context.Context, onProgress gameserver.ProgressFunc) error {
		return svc.LifecycleSvc.Start(ctx, gs.ID, onProgress)
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an operation in progress")
}

func TestActivity_StopBypassesMutex(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID, nil))

	// Set an operation in progress
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.OperationType = ptrStr(model.OpBackup)
	fetched.OperationID = ptrStr("fake-op")
	require.NoError(t, store.New(svc.DB).UpdateGameserver(fetched))

	// Stop should still work despite the running operation (stop bypasses the guard)
	err := svc.Runner.Submit(gs.ID, model.OpStop, event.Actor{Type: "test"}, func(ctx context.Context, _ gameserver.ProgressFunc) error {
		return svc.LifecycleSvc.Stop(ctx, gs.ID)
	})
	require.NoError(t, err)
	svc.Runner.Wait()
}

func TestActivity_RestartCreatesOneEvent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID, nil))

	// Clear events from the start
	svc.DB.Exec("DELETE FROM events")

	err := svc.Runner.Submit(gs.ID, model.OpRestart, event.Actor{Type: "test"}, func(ctx context.Context, onProgress gameserver.ProgressFunc) error {
		return svc.LifecycleSvc.Restart(ctx, gs.ID, onProgress)
	})
	require.NoError(t, err)
	svc.Runner.Wait()

	s := store.New(svc.DB)
	events, err := s.ListEvents(model.EventFilter{GameserverID: &gs.ID})
	require.NoError(t, err)

	// Should have exactly one event (restart), not three (restart + stop + start)
	assert.Len(t, events, 1, "restart should create a single event, not nested ones")
	assert.Equal(t, "gameserver.restart", events[0].Type)
}

func TestActivity_ClearStaleOperations(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	s := store.New(svc.DB)

	// Simulate a stale operation from a crash
	gs.OperationType = ptrStr(model.OpBackup)
	gs.OperationID = ptrStr("stale-op")
	require.NoError(t, s.UpdateGameserver(gs))

	cleared, err := s.ClearStaleOperations()
	require.NoError(t, err)
	assert.Equal(t, 1, cleared)

	fetched, _ := s.GetGameserver(gs.ID)
	assert.Nil(t, fetched.OperationType)
	assert.Nil(t, fetched.OperationID)
}

func TestActivity_BackupBlocksStart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	ctx := testutil.TestContext()

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID, nil))

	// Trigger a backup
	_, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "test-backup")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Check if operation is set
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	if fetched.OperationType != nil {
		err = svc.Runner.Submit(gs.ID, model.OpRestart, event.Actor{Type: "test"}, func(ctx context.Context, onProgress gameserver.ProgressFunc) error {
			return svc.LifecycleSvc.Restart(ctx, gs.ID, onProgress)
		})
		assert.Error(t, err, "restart should be rejected while backup is running")
		assert.Contains(t, err.Error(), "already has an operation in progress")
	} else {
		t.Log("backup completed before we could test mutex — skipping concurrent check")
	}
}

func TestActivity_MultipleStopsAllowed(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)
	ctx := testutil.TestContext()

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID, nil))
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))
}

func ptrStr(s string) *string { return &s }
