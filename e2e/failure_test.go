//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_Crash_WhileRunning verifies that a gameserver process crashing after
// reaching "running" is detected and transitions to "error" status, and that
// a subsequent start recovers cleanly.
func TestE2E_Crash_WhileRunning(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR": "crash-after-ready",
	}))
	startAndWaitRunning(t, h, gs.ID)

	// Process crashes after ~2 seconds — should transition to error
	require.NoError(t, h.WaitForStatus(gs.ID, "error", 30*time.Second),
		"crashed gameserver should reach error status")

	// Patch env to remove crash behavior, then restart
	resp, err := h.Patch("/api/gameservers/"+gs.ID, map[string]any{
		"env": testGameEnv(h, nil),
	})
	require.NoError(t, err)
	resp.Body.Close()

	startAndWaitRunning(t, h, gs.ID)
	t.Logf("gameserver recovered after crash")
}

// TestE2E_Crash_BeforeReady verifies that a process that crashes before emitting
// the ready pattern ends up in "error" and does not hang in "starting" forever.
func TestE2E_Crash_BeforeReady(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR": "crash-before-ready",
	}))

	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// Should reach error, not hang in starting/installing
	require.NoError(t, h.WaitForStatus(gs.ID, "error", 30*time.Second),
		"gameserver that crashes before ready should reach error status, not hang")
}

// TestE2E_InstallFailure_RecoversOnRetry verifies that an install failure
// produces an error status, and that fixing the env and retrying succeeds.
func TestE2E_InstallFailure_RecoversOnRetry(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_INSTALL_BEHAVIOR": "fail",
	}))

	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// Install script exits 1 — should reach error
	require.NoError(t, h.WaitForStatus(gs.ID, "error", 30*time.Second),
		"failed install should produce error status")

	// Fix env and retry
	resp, err = h.Patch("/api/gameservers/"+gs.ID, map[string]any{
		"env": testGameEnv(h, nil),
	})
	require.NoError(t, err)
	resp.Body.Close()

	startAndWaitRunning(t, h, gs.ID)
	t.Logf("gameserver started after install retry")
}

// TestE2E_Stop_IgnoresSigterm_EventuallyKilled verifies that a gameserver
// process which ignores SIGTERM is eventually force-killed and reaches "stopped".
func TestE2E_Stop_IgnoresSigterm_EventuallyKilled(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR": "ignore-sigterm",
	}))
	startAndWaitRunning(t, h, gs.ID)

	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// Should still reach stopped via SIGKILL after the graceful timeout
	require.NoError(t, h.WaitForStatus(gs.ID, "stopped", 2*time.Minute),
		"gameserver that ignores SIGTERM should still be force-stopped")
	t.Logf("gameserver force-stopped after ignoring SIGTERM")
}

// TestE2E_SelfExit_DetectedAsError verifies that a gameserver process which
// exits cleanly on its own (without a stop request) is detected as an error.
func TestE2E_SelfExit_DetectedAsError(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR": "exit-clean",
	}))
	startAndWaitRunning(t, h, gs.ID)

	// Process exits cleanly after ~2 seconds — should be treated as unexpected
	status, err := waitForStatusOneOf(h, gs.ID, []string{"error", "stopped"}, 30*time.Second)
	require.NoError(t, err, "unexpected self-exit should be detected")
	assert.Equal(t, "error", status,
		"unexpected clean exit should be marked as error, not stopped")
}
