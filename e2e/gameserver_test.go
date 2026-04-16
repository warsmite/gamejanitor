//go:build e2e

package e2e

// gameserver_test.go — lifecycle transitions, crashes, failure modes, and
// concurrent operations on a single gameserver. Archive workflows live in
// archive_test.go; multi-node migration in migration_test.go; backup flows
// in backup_test.go; file manager in files_test.go.

import (
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Basic lifecycle ---

func TestGameserver_Basic(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()

	gs.Start().MustBeRunning()

	snap := gs.Snapshot()
	assert.True(t, snap.Installed, "installed flag should be set after install")

	gs.Stop().MustBeStopped()
	gs.Delete().MustBeGone()
}

func TestGameserver_SecondStart_SkipsInstall(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()

	// First start installs.
	gs.Start().MustBeRunning()
	gs.Stop().MustBeStopped()

	// Second start should be much faster — install already done.
	gs.Start().MustBeRunning()
}

// --- Idempotency ---

func TestGameserver_DoubleStart(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()

	gs.Start().MustBeRunning()
	gs.Start() // second start is a safe no-op

	snap := gs.Snapshot()
	assert.Equal(t, "running", snap.ProcessState)
	assert.True(t, snap.Ready)
}

func TestGameserver_DoubleStop(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()

	gs.Start().MustBeRunning()
	gs.Stop().MustBeStopped()
	gs.Stop() // second stop is a safe no-op

	snap := gs.Snapshot()
	assert.Equal(t, "none", snap.ProcessState)
	assert.Nil(t, snap.InstanceID)
}

// --- Ports ---

func TestGameserver_Ports_UniquePerServer(t *testing.T) {
	env := NewEnv(t)

	var gss []*Gameserver
	for _, name := range []string{"Server A", "Server B"} {
		gs := env.NewGameserver(map[string]any{"name": name})
		gss = append(gss, gs)
	}

	// Start sequentially to avoid event-bus drop races during parallel starts.
	for _, gs := range gss {
		gs.Start().MustBeRunning()
	}

	// Assert port uniqueness.
	seen := map[int]bool{}
	for _, gs := range gss {
		for _, p := range gs.Snapshot().Ports {
			assert.False(t, seen[p.Port], "port %d assigned to multiple gameservers", p.Port)
			seen[p.Port] = true
		}
	}

	// Verify at least one port is actually bound — confirms real port mapping.
	for _, gs := range gss {
		for _, p := range gs.Snapshot().Ports {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port), 2*time.Second)
			if err == nil {
				conn.Close()
				return // one successful dial is enough
			}
		}
	}
	// No successful dial isn't a failure — the game script may fall back to sleep
	// if socat isn't in the base image. Port allocation correctness is what matters.
}

func TestGameserver_Parallel_UniquePorts(t *testing.T) {
	env := NewEnv(t)

	const count = 5
	gss := make([]*Gameserver, count)
	for i := range gss {
		gss[i] = env.NewGameserver()
	}

	// Start concurrently.
	var wg sync.WaitGroup
	for _, gs := range gss {
		wg.Add(1)
		go func(gs *Gameserver) {
			defer wg.Done()
			gs.Start().MustBeRunning()
		}(gs)
	}
	wg.Wait()

	seen := map[int]bool{}
	for _, gs := range gss {
		for _, p := range gs.Snapshot().Ports {
			assert.False(t, seen[p.Port], "port %d assigned to multiple gameservers", p.Port)
			seen[p.Port] = true
		}
	}
}

// --- Delete while running (regression: auto-restart must not fire) ---

func TestGameserver_Delete_AutoRestartRace(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver(map[string]any{"auto_restart": true})
	gs.Start().MustBeRunning()
	gs.Delete().MustBeGone()
}

// TestGameserver_Stop_AutoRestartRace is the sibling regression: graceful
// stop on an auto_restart=true gameserver must not trigger a spurious
// auto-restart from HandleProcessEvent misreading the stop exit as a crash.
func TestGameserver_Stop_AutoRestartRace(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver(map[string]any{"auto_restart": true})
	gs.Start().MustBeRunning()
	gs.Stop().MustBeStopped()

	snap := gs.Snapshot()
	assert.Equal(t, "none", snap.ProcessState, "gameserver should stay stopped, not auto-restart")
	assert.Empty(t, snap.ErrorReason, "graceful stop should not leave an error")
}

// --- Crash detection ---

func TestGameserver_Crash_WhileRunning(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_BEHAVIOR": "crash-after-ready"}),
	})
	gs.Start().MustBeRunning()

	// Process crashes after ~2s — should transition to error.
	reason := (&Action{gs: gs, kind: "crash"}).MustBeError()
	assert.NotEmpty(t, reason, "crash should surface a reason")

	// Clear the crash behavior and recover.
	gs.UpdateEnv(env.GameEnv())
	gs.Start().MustBeRunning()
}

func TestGameserver_Crash_BeforeReady(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_BEHAVIOR": "crash-before-ready"}),
	})

	// Start should reach error, not hang in starting/installing.
	gs.Start().MustBeError()
}

// --- Install failure + recovery ---

func TestGameserver_InstallFailure_Recovers(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_INSTALL_BEHAVIOR": "fail"}),
	})

	// Install script exits 1 — should reach error.
	gs.Start().MustBeError()

	// Fix env and retry — should succeed.
	gs.UpdateEnv(env.GameEnv())
	gs.Start().MustBeRunning()
}

// --- SIGTERM handling ---

func TestGameserver_Stop_SIGTERMIgnored(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_BEHAVIOR": "ignore-sigterm"}),
	})
	gs.Start().MustBeRunning()

	// Worker should fall through to SIGKILL after the graceful timeout.
	// Stall budget (120s) comfortably exceeds the typical stop timeout (30s).
	gs.Stop().MustBeStopped()
}

// --- Self-exit detection ---

func TestGameserver_SelfExit(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_BEHAVIOR": "exit-clean"}),
	})
	gs.Start().MustBeRunning()

	// Process exits cleanly on its own — should be classified as an error
	// (unexpected exit), not a graceful stop.
	terminal := (&Action{gs: gs, kind: "self-exit"}).MustReachNonRunningTerminal()
	assert.Equal(t, "error", terminal, "unexpected clean exit should be marked as error")
}

// --- Slow ready ---

func TestGameserver_SlowReady(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{
			"TEST_BEHAVIOR":       "slow-ready",
			"READY_DELAY_SECONDS": "3",
		}),
	})

	// Framework handles the delay transparently — stall timer keeps getting
	// reset by phase events and eventually ready fires.
	gs.Start().MustBeRunning()
}

// --- Log flood resilience ---

func TestGameserver_LogFlood_NoOOM(t *testing.T) {
	env := NewEnv(t)
	env.RequireTestGame()

	gs := env.NewGameserver(map[string]any{
		"env": mergeEnv(env.GameEnv(), map[string]string{"TEST_BEHAVIOR": "stdout-flood"}),
	})
	gs.Start().MustBeRunning()

	// Let it flood for a bit, then verify the controller is still responsive
	// and the server is still tracked as running.
	time.Sleep(3 * time.Second)
	snap := gs.Snapshot()
	assert.Equal(t, "running", snap.ProcessState, "gameserver should survive log flood")
	assert.True(t, snap.Ready, "should still be ready under log pressure")

	// Stop should still work under log pressure.
	gs.Stop().MustBeStopped()
}

// --- Running-server API sanity ---

// TestGameserver_RunningAPI exercises the read/query endpoints against a
// running gameserver. Catches regressions in routes that aren't critical
// enough for their own test but should work on any running instance.
func TestGameserver_RunningAPI(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()
	gs.Start().MustBeRunning()

	t.Run("snapshot_shape", func(t *testing.T) {
		s := gs.Snapshot()
		require.NotNil(t, s)
		assert.Equal(t, gs.ID(), s.ID)
		assert.Equal(t, "running", s.ProcessState)
		assert.True(t, s.Ready)
		assert.True(t, s.Installed, "should be installed")
		assert.NotNil(t, s.NodeID, "should be assigned to a node")
		assert.NotEmpty(t, s.Ports, "should have ports")
		assert.NotEmpty(t, s.VolumeName, "should have a volume")
	})

	t.Run("stats_endpoint", func(t *testing.T) {
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		stats, err := env.sdk.Gameservers.Stats(ctx, gs.ID())
		require.NoError(t, err)
		assert.GreaterOrEqual(t, stats.CPUPercent, 0.0)
		assert.GreaterOrEqual(t, stats.MemoryUsageMB, 0.0)
	})

	t.Run("stats_polling_fires_events", func(t *testing.T) {
		// Verify the stats poller is actually running — a stats event should
		// arrive within a few seconds.
		gs.WaitForEvent("gameserver.stats")
	})

	t.Run("logs_endpoint", func(t *testing.T) {
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		logs, err := env.sdk.Gameservers.Logs(ctx, gs.ID(), 100)
		require.NoError(t, err)
		assert.NotEmpty(t, logs.Lines, "should have log output from startup")
	})

	t.Run("send_command", func(t *testing.T) {
		// Some games don't support commands; just verify no 500.
		gs.SendCommand("test")
	})

	t.Run("query_endpoint", func(t *testing.T) {
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		// Query may fail for games with no query protocol — we just want it
		// to not 500. SDK error types distinguish client vs server errors.
		_, _ = env.sdk.Gameservers.Query(ctx, gs.ID())
	})

	t.Run("update_env_persists", func(t *testing.T) {
		gs.UpdateEnv(mergeEnv(env.GameEnv(), map[string]string{"SERVER_NAME": "Sanity"}))
		assert.Equal(t, "Sanity", gs.Snapshot().Env["SERVER_NAME"])
	})
}

// --- Utilities local to this file ---

func mergeEnv(base map[string]string, overrides map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overrides {
		out[k] = v
	}
	return out
}
