//go:build e2e

package e2e

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_RunningServer_APISanity spins up a single gameserver and exercises
// every read/query API against it. Catches regressions in endpoints that aren't
// critical enough for their own test but should work on a running instance.
func TestE2E_RunningServer_APISanity(t *testing.T) {
	h := Start(t)

	gs := createGameserver(t, h, testGameEnv(h, nil))
	startAndWaitRunning(t, h, gs.ID)

	t.Run("detail", func(t *testing.T) {
		resp, err := h.Get("/api/gameservers/" + gs.ID)
		require.NoError(t, err)

		var detail struct {
			ID        string  `json:"id"`
			Status    string  `json:"status"`
			Installed bool    `json:"installed"`
			NodeID    *string `json:"node_id"`
			Ports     []struct {
				Name     string `json:"name"`
				HostPort int    `json:"host_port"`
				Protocol string `json:"protocol"`
			} `json:"ports"`
			Env         map[string]string `json:"env"`
			VolumeName  string            `json:"volume_name"`
			AutoRestart bool              `json:"auto_restart"`
		}
		require.NoError(t, DecodeData(resp, &detail))

		assert.Equal(t, gs.ID, detail.ID)
		assert.Equal(t, "running", detail.Status)
		assert.True(t, detail.Installed, "should be installed after start")
		assert.NotNil(t, detail.NodeID, "should be assigned to a node")
		assert.NotEmpty(t, detail.Ports, "should have ports allocated")
		assert.NotEmpty(t, detail.VolumeName, "should have a volume")

		for _, p := range detail.Ports {
			assert.Greater(t, p.HostPort, 0, "port %s should have a host port", p.Name)
			assert.NotEmpty(t, p.Protocol, "port %s should have a protocol", p.Name)
		}
	})

	t.Run("stats", func(t *testing.T) {
		resp, err := h.Get("/api/gameservers/" + gs.ID + "/stats")
		require.NoError(t, err)

		var stats struct {
			CPUPercent    float64 `json:"cpu_percent"`
			MemoryUsageMB float64 `json:"memory_usage_mb"`
		}
		require.NoError(t, DecodeData(resp, &stats))

		assert.GreaterOrEqual(t, stats.CPUPercent, 0.0, "cpu should be non-negative")
		assert.GreaterOrEqual(t, stats.MemoryUsageMB, 0.0, "memory should be non-negative")
	})

	t.Run("logs", func(t *testing.T) {
		resp, err := h.Get("/api/gameservers/" + gs.ID + "/logs")
		require.NoError(t, err)

		var logs struct {
			Lines []string `json:"lines"`
		}
		require.NoError(t, DecodeData(resp, &logs))

		assert.NotEmpty(t, logs.Lines, "should have log output from startup")
	})

	t.Run("send_command", func(t *testing.T) {
		resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/actions/command", map[string]any{
			"command": "test",
		})
		require.NoError(t, err)
		resp.Body.Close()

		// Some games don't support commands — accept both success and
		// a structured error, but not a 500.
		assert.Less(t, resp.StatusCode, 500, "command should not cause a server error")
	})

	t.Run("file_list", func(t *testing.T) {
		files := h.ListFiles(t, gs.ID, "/data")
		assert.NotEmpty(t, files, "volume should have files after install")
	})

	t.Run("update_env", func(t *testing.T) {
		newEnv := testGameEnv(h, map[string]string{"SERVER_NAME": "Sanity Check"})
		resp, err := h.Patch("/api/gameservers/"+gs.ID, map[string]any{
			"env": newEnv,
		})
		require.NoError(t, err)
		resp.Body.Close()
		assert.Less(t, resp.StatusCode, 400, "env update should succeed")

		// Verify it persisted
		getResp, err := h.Get("/api/gameservers/" + gs.ID)
		require.NoError(t, err)
		var updated struct {
			Env map[string]string `json:"env"`
		}
		require.NoError(t, DecodeData(getResp, &updated))
		assert.Equal(t, "Sanity Check", updated.Env["SERVER_NAME"])
	})

	t.Run("activity", func(t *testing.T) {
		resp, err := h.Get("/api/activity?gameserver_id=" + gs.ID)
		require.NoError(t, err)

		var events []struct {
			Type         string `json:"type"`
			GameserverID string `json:"gameserver_id"`
		}
		require.NoError(t, DecodeData(resp, &events))

		assert.NotEmpty(t, events, "should have activity entries after start")

		// Verify at least one event belongs to this gameserver
		found := false
		for _, e := range events {
			if e.GameserverID == gs.ID {
				found = true
				break
			}
		}
		assert.True(t, found, "activity should include events for this gameserver")
	})

	t.Run("log_sessions", func(t *testing.T) {
		resp, err := h.Get("/api/gameservers/" + gs.ID + "/logs/sessions")
		require.NoError(t, err)

		var sessions []struct {
			Session int `json:"session"`
		}
		require.NoError(t, DecodeData(resp, &sessions))

		assert.NotEmpty(t, sessions, "should have at least one log session")
	})

	t.Run("query", func(t *testing.T) {
		resp, err := h.Get("/api/gameservers/" + gs.ID + "/query")
		require.NoError(t, err)
		resp.Body.Close()

		// Query may not work for all games (no query protocol, port not
		// reachable from test runner). Just verify the endpoint doesn't 500.
		assert.Less(t, resp.StatusCode, 500, "query endpoint should not cause a server error")
	})
}
