package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
	"github.com/warsmite/gamejanitor/worker"
)

// TestRace_StartStopStart verifies that a rapid start → stop → start sequence
// completes without the operation guard rejecting the second start.
//
// The race: the runner clears operation_type AFTER the lifecycle method returns.
// If the second start is submitted between stop completing (desired_state=stopped)
// and the runner calling activity.Complete (operation_type=null), the guard rejects it.
func TestRace_StartStopStart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	actor := event.Actor{Type: "test"}

	// First start — through the runner (async) like the real API handler
	err := svc.Runner.Submit(gs.ID, model.OpStart, actor, func(ctx context.Context, onProgress operation.ProgressFunc) error {
		return svc.LifecycleSvc.Start(ctx, gs.ID, onProgress)
	})
	require.NoError(t, err)
	svc.Runner.Wait()

	// Verify running
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.InstanceID, "should have an instance after start")

	// Stop — through the runner
	err = svc.Runner.Submit(gs.ID, model.OpStop, actor, func(ctx context.Context, _ operation.ProgressFunc) error {
		return svc.LifecycleSvc.Stop(ctx, gs.ID)
	})
	require.NoError(t, err)
	svc.Runner.Wait()

	// Verify stopped and operation cleared
	fetched, err = svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched.InstanceID, "instance should be nil after stop")
	assert.Nil(t, fetched.OperationType, "operation_type should be cleared after stop")

	// Second start — should succeed, not be rejected by operation guard
	err = svc.Runner.Submit(gs.ID, model.OpStart, actor, func(ctx context.Context, onProgress operation.ProgressFunc) error {
		return svc.LifecycleSvc.Start(ctx, gs.ID, onProgress)
	})
	require.NoError(t, err, "second start after stop should be accepted by operation guard")
	svc.Runner.Wait()

	fetched, err = svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.NotNil(t, fetched.InstanceID, "should have an instance after second start")
}

// TestRace_StopSettlesCleanly verifies that stopping a running gameserver
// results in "stopped" status, not "error" — even when the exit event
// (with non-zero exit code from SIGKILL) races with the stop cleanup.
//
// The race: StopInstance sends SIGTERM→SIGKILL, the worker fires an exit event
// with non-zero exit code. If the StatusManager processes this event before
// stopInstance finishes, DeriveStatus could briefly show "error".
func TestRace_StopSettlesCleanly(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServicesWithSubscribers(t)
	fw := testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	// Start the gameserver
	require.NoError(t, svc.LifecycleSvc.Start(ctx, gs.ID, nil))

	// Verify it's running with a real instance
	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.InstanceID)

	// Inject running state into StatusManager so DeriveStatus returns "running"
	svc.StatusMgr.InjectWorkerState(gs.ID, &worker.InstanceStateUpdate{
		InstanceID: *fetched.InstanceID,
		State:      worker.StateRunning,
		StartedAt:  time.Now(),
	})

	// Verify status is "running" before we stop
	fetched, err = svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "running", fetched.Status)

	// Stop — this triggers the SIGTERM→exit sequence
	require.NoError(t, svc.LifecycleSvc.Stop(ctx, gs.ID))

	// Give the event bus a moment to process any exit events
	time.Sleep(100 * time.Millisecond)

	// Status should be "stopped", NOT "error"
	fetched, err = svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "stopped", fetched.Status, "stop should settle to stopped, not error")
	assert.Empty(t, fetched.ErrorReason, "should have no error reason after clean stop")
	assert.Nil(t, fetched.InstanceID, "instance should be nil after stop")

	// Verify the instance was actually removed
	assert.Equal(t, 0, fw.InstanceCount(), "all instances should be removed after stop")
}
