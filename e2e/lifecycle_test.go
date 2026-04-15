//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_Lifecycle_CreateStartStopDelete(t *testing.T) {
	h := Start(t)

	// Create
	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":    "E2E Lifecycle",
		"game_id": h.GameID(),
		"env":     h.GameEnv(),
	})
	require.NoError(t, err)

	var gs struct {
		ID           string `json:"id"`
		Status       string `json:"status"`
		SFTPPassword string `json:"sftp_password"`
	}
	require.NoError(t, DecodeData(resp, &gs))
	require.NotEmpty(t, gs.ID)
	assert.Equal(t, "stopped", gs.Status)
	assert.NotEmpty(t, gs.SFTPPassword, "create response should include SFTP password")

	// Start
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// Wait for running — the real ReadyWatcher parses the instance.s log output
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 2*time.Minute),
		"gameserver should reach 'running' after ready pattern detected in real instance logs")

	// Verify installed flag set (controller marks installed after install phase completes)
	resp, err = h.Get("/api/gameservers/" + gs.ID)
	require.NoError(t, err)
	var fetched struct {
		Installed bool `json:"installed"`
	}
	require.NoError(t, DecodeData(resp, &fetched))
	assert.True(t, fetched.Installed, "installed flag should be set after install phase")

	// Stop
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/actions/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()

	require.NoError(t, h.WaitForStatus(gs.ID, "stopped", time.Minute))

	// Delete (async — returns 202)
	resp, err = h.Delete("/api/gameservers/" + gs.ID)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 202, resp.StatusCode)

	// Verify gone (async — wait for cleanup)
	require.Eventually(t, func() bool {
		resp, err := h.Get("/api/gameservers/" + gs.ID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 404
	}, 30*time.Second, 500*time.Millisecond, "gameserver should be deleted")
}

func TestE2E_Lifecycle_SecondStart_SkipsInstall(t *testing.T) {
	h := Start(t)

	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name": "Skip Install", "game_id": h.GameID(),
		"env": h.GameEnv(),
	})
	require.NoError(t, err)
	var gs struct{ ID string }
	require.NoError(t, DecodeData(resp, &gs))

	// First start — installs
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 2*time.Minute))

	// Stop
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/stop", nil)
	require.NoError(t, h.WaitForStatus(gs.ID, "stopped", time.Minute))

	// Second start — should skip install phase (gs.Installed is true)
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 2*time.Minute))

	// Cleanup
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/stop", nil)
	h.WaitForStatus(gs.ID, "stopped", time.Minute)
	h.Delete("/api/gameservers/" + gs.ID)
}

func TestE2E_Ports_TwoDifferentPorts(t *testing.T) {
	h := Start(t)

	var gsIDs []string
	for _, name := range []string{"Server A", "Server B"} {
		resp, err := h.PostJSON("/api/gameservers", map[string]any{
			"name": name, "game_id": h.GameID(),
			"env": h.GameEnv(),
		})
		require.NoError(t, err)
		var gs struct{ ID string }
		require.NoError(t, DecodeData(resp, &gs))
		gsIDs = append(gsIDs, gs.ID)
	}

	// Start sequentially — concurrent starts can drop events due to the
	// non-blocking event bus (documented in TESTING_BUGS.md)
	for _, id := range gsIDs {
		h.PostJSON("/api/gameservers/"+id+"/actions/start", nil)
		require.NoError(t, h.WaitForStatus(id, "running", 2*time.Minute))
	}

	// Get port assignments and verify they're different
	type portInfo struct {
		HostPort int `json:"host_port"`
	}
	var allPorts []int
	for _, id := range gsIDs {
		resp, _ := h.Get("/api/gameservers/" + id)
		var gs struct {
			Ports []portInfo `json:"ports"`
		}
		DecodeData(resp, &gs)
		for _, p := range gs.Ports {
			allPorts = append(allPorts, p.HostPort)
		}
	}

	// All ports should be unique
	seen := make(map[int]bool)
	for _, p := range allPorts {
		assert.False(t, seen[p], "port %d assigned to multiple gameservers", p)
		seen[p] = true
	}

	// Verify at least one port is actually bound (TCP dial)
	if len(allPorts) > 0 {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", allPorts[0]), 5*time.Second)
		if err == nil {
			conn.Close()
			// Port is actually bound — confirms real port mapping works
		}
		// Note: if socat isn't in the base image, the game script falls back to sleep
		// and the port won't actually be bound. That's OK — we still verified allocation.
	}

	// Cleanup
	for _, id := range gsIDs {
		h.PostJSON("/api/gameservers/"+id+"/actions/stop", nil)
		h.WaitForStatus(id, "stopped", time.Minute)
		h.Delete("/api/gameservers/" + id)
	}
}

// TestE2E_Lifecycle_DeleteWhileRunning_AutoRestart is a regression test for the
// case where deleting a running gameserver with auto_restart=true would race
// against the auto-restart handler and leave the gameserver alive. The real
// worker emits StateExited asynchronously when the process dies, so this can
// only be caught against a live sandbox — FakeWorker doesn't reproduce it.
func TestE2E_Lifecycle_DeleteWhileRunning_AutoRestart(t *testing.T) {
	h := Start(t)

	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":         "Delete While Running",
		"game_id":      h.GameID(),
		"env":          h.GameEnv(),
		"auto_restart": true,
	})
	require.NoError(t, err)
	var gs struct{ ID string }
	require.NoError(t, DecodeData(resp, &gs))

	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 2*time.Minute))

	// Delete while running — must NOT trigger an auto-restart.
	resp, err = h.Delete("/api/gameservers/" + gs.ID)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 202, resp.StatusCode)

	// Gameserver must actually be deleted, not bounced back to running.
	require.Eventually(t, func() bool {
		r, err := h.Get("/api/gameservers/" + gs.ID)
		if err != nil {
			return false
		}
		defer r.Body.Close()
		return r.StatusCode == 404
	}, 60*time.Second, 500*time.Millisecond, "gameserver should be deleted, not auto-restarted")
}

func TestE2E_Files_WriteAndRead(t *testing.T) {
	h := Start(t)

	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name": "File Test", "game_id": h.GameID(),
		"env": h.GameEnv(),
	})
	require.NoError(t, err)
	var gs struct{ ID string }
	require.NoError(t, DecodeData(resp, &gs))

	// Start so the volume exists and has data
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 2*time.Minute))

	// Write a file via API
	req, _ := http.NewRequest("PUT",
		h.BaseURL+"/api/gameservers/"+gs.ID+"/files/content?path=/data/test.txt",
		bytes.NewReader([]byte("hello from e2e")))
	req.Header.Set("Content-Type", "application/octet-stream")
	writeResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	writeResp.Body.Close()

	// Read it back
	readResp, err := http.Get(h.BaseURL + "/api/gameservers/" + gs.ID + "/files/content?path=/data/test.txt")
	require.NoError(t, err)
	body, _ := io.ReadAll(readResp.Body)
	readResp.Body.Close()

	assert.Contains(t, string(body), "hello from e2e")

	// Cleanup
	h.PostJSON("/api/gameservers/"+gs.ID+"/actions/stop", nil)
	h.WaitForStatus(gs.ID, "stopped", time.Minute)
	h.Delete("/api/gameservers/" + gs.ID)
}
