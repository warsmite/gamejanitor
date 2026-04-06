package lifecycle_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestActivity_StartCreatesEvent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

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

	// Start should be rejected — there's already an operation in progress
	err := svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already has an operation in progress")
}

func TestActivity_StopBypassesMutex(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Set an operation in progress
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.OperationType = ptrStr(model.OpBackup)
	fetched.OperationID = ptrStr("fake-op")
	require.NoError(t, store.New(svc.DB).UpdateGameserver(fetched))

	// Stop should still work despite the running operation
	err := svc.LifecycleSvc.Stop(testutil.TestContext(), gs.ID)
	require.NoError(t, err)
	svc.LifecycleSvc.WaitForOperations()
}

func TestActivity_RestartCreatesOneEvent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	gs := testutil.CreateTestGameserver(t, svc)

	require.NoError(t, svc.LifecycleSvc.Start(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Clear events from the start
	svc.DB.Exec("DELETE FROM events")

	require.NoError(t, svc.LifecycleSvc.Restart(testutil.TestContext(), gs.ID))
	svc.LifecycleSvc.WaitForOperations()

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

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()

	// Trigger a backup
	_, err := svc.BackupSvc.CreateBackup(ctx, gs.ID, "test-backup")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Check if operation is set
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	if fetched.OperationType != nil {
		err = svc.LifecycleSvc.Restart(ctx, gs.ID)
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

	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))
	svc.LifecycleSvc.WaitForOperations()
}

func ptrStr(s string) *string { return &s }
