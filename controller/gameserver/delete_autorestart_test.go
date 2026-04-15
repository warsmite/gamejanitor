package gameserver_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

// TestDelete_RunningGameserver_WithAutoRestart is a regression test for the bug
// where deleting a running gameserver with auto_restart=true triggered a
// spurious auto-restart instead of completing the delete. Delete now bypasses
// the graceful Stop flow and marks the gameserver as OpDelete before any worker
// calls so process-exit events don't get interpreted as unexpected crashes.
func TestDelete_RunningGameserver_WithAutoRestart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	autoRestart := true
	gs := &model.Gameserver{
		Name:        "auto-restart delete",
		GameID:      testutil.TestGameID,
		Env:         model.Env{"REQUIRED_VAR": "test-value"},
		AutoRestart: &autoRestart,
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Start(ctx))
	live.WaitForOperation()
	require.Eventually(t, func() bool {
		return live.Snapshot().Status == "running"
	}, 2*time.Second, 10*time.Millisecond, "precondition: gameserver should reach running")

	require.NoError(t, svc.Manager.Delete(ctx, gs.ID), "delete should succeed")

	gone, _ := svc.Manager.GetGameserver(gs.ID)
	assert.Nil(t, gone, "gameserver should be deleted, not auto-restarted")
	assert.Nil(t, svc.Manager.Get(gs.ID), "live gameserver should be removed from manager")

	// Give any in-flight auto-restart goroutine a moment to expose itself.
	time.Sleep(100 * time.Millisecond)
	assert.Nil(t, svc.Manager.Get(gs.ID), "gameserver should stay deleted")
}

// TestStop_RunningGameserver_WithAutoRestart verifies that a graceful Stop on
// an auto_restart=true server doesn't get overridden by the auto-restart
// handler. HandleProcessEvent classifies exits as intentional when
// desiredState != "running" (doStop flips it before any worker calls).
func TestStop_RunningGameserver_WithAutoRestart(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	autoRestart := true
	gs := &model.Gameserver{
		Name:        "auto-restart stop",
		GameID:      testutil.TestGameID,
		Env:         model.Env{"REQUIRED_VAR": "test-value"},
		AutoRestart: &autoRestart,
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	live := svc.Manager.Get(gs.ID)
	require.NotNil(t, live)

	require.NoError(t, live.Start(ctx))
	live.WaitForOperation()
	require.Eventually(t, func() bool {
		return live.Snapshot().Status == "running"
	}, 2*time.Second, 10*time.Millisecond, "precondition: gameserver should reach running")

	require.NoError(t, live.Stop(ctx))

	assert.Equal(t, "stopped", live.Snapshot().Status, "stop should leave the gameserver stopped")
	assert.Empty(t, live.Snapshot().ErrorReason, "graceful stop should not set an error reason")

	// Wait briefly to catch any delayed auto-restart goroutine.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "stopped", live.Snapshot().Status, "gameserver should stay stopped")
}
