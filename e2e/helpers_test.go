//go:build e2e

package e2e

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type gsInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type backupInfo struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// createGameserver creates a test-game gameserver with the given env vars.
func createGameserver(t *testing.T, h *Harness, env map[string]string) gsInfo {
	t.Helper()
	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":    t.Name(),
		"game_id": h.GameID(),
		"env":     env,
	})
	require.NoError(t, err)

	var gs gsInfo
	require.NoError(t, DecodeData(resp, &gs))
	require.NotEmpty(t, gs.ID)

	t.Cleanup(func() {
		// Best-effort cleanup: stop then delete
		if resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/stop", nil); err == nil {
			resp.Body.Close()
		}
		h.WaitForStatus(gs.ID, "stopped", 30*time.Second)
		if resp, err := h.Delete("/api/gameservers/" + gs.ID); err == nil {
			resp.Body.Close()
		}
	})

	return gs
}

// startAndWaitRunning starts a gameserver and waits for "running" status.
// Uses a 5-minute timeout to accommodate real games under parallel contention.
func startAndWaitRunning(t *testing.T, h *Harness, gsID string) {
	t.Helper()
	resp, err := h.PostJSON("/api/gameservers/"+gsID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.NoError(t, h.WaitForStatus(gsID, "running", 5*time.Minute),
		"gameserver should reach running")
}

// stopAndWaitStopped stops a gameserver and waits for "stopped" status.
func stopAndWaitStopped(t *testing.T, h *Harness, gsID string) {
	t.Helper()
	resp, err := h.PostJSON("/api/gameservers/"+gsID+"/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.NoError(t, h.WaitForStatus(gsID, "stopped", time.Minute),
		"gameserver should reach stopped")
}

// deleteAndWaitGone deletes a gameserver and waits until it returns 404.
func deleteAndWaitGone(t *testing.T, h *Harness, gsID string) {
	t.Helper()
	resp, err := h.Delete("/api/gameservers/" + gsID)
	require.NoError(t, err)
	resp.Body.Close()

	require.Eventually(t, func() bool {
		resp, err := h.Get("/api/gameservers/" + gsID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 404
	}, 30*time.Second, 500*time.Millisecond, "gameserver should be deleted")
}

// writeFile uploads content to a file path on the gameserver volume.
func writeFile(t *testing.T, h *Harness, gsID string, path string, content string) {
	t.Helper()
	req, err := http.NewRequest("PUT",
		h.BaseURL+"/api/gameservers/"+gsID+"/files/content?path="+path,
		bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 400, "writeFile should succeed")
}

// readFile reads a file from the gameserver volume and returns its content.
func readFile(t *testing.T, h *Harness, gsID string, path string) string {
	t.Helper()
	resp, err := http.Get(h.BaseURL + "/api/gameservers/" + gsID + "/files/content?path=" + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return string(body)
}

// createBackup creates a backup and returns its info.
func createBackup(t *testing.T, h *Harness, gsID string) backupInfo {
	t.Helper()
	resp, err := h.PostJSON("/api/gameservers/"+gsID+"/backups", map[string]any{
		"name": "e2e-" + t.Name(),
	})
	require.NoError(t, err)

	var b backupInfo
	require.NoError(t, DecodeData(resp, &b))
	require.NotEmpty(t, b.ID)
	return b
}

// waitForBackupComplete polls until a backup reaches "completed" or "failed".
func waitForBackupComplete(t *testing.T, h *Harness, gsID string, backupID string) {
	t.Helper()
	require.Eventually(t, func() bool {
		resp, err := h.Get("/api/gameservers/" + gsID + "/backups")
		if err != nil {
			return false
		}
		var backups []backupInfo
		if err := DecodeData(resp, &backups); err != nil {
			return false
		}
		for _, b := range backups {
			if b.ID == backupID && (b.Status == "completed" || b.Status == "failed") {
				return b.Status == "completed"
			}
		}
		return false
	}, time.Minute, time.Second, "backup should complete")
}

// restoreBackup restores a backup to a gameserver.
func restoreBackup(t *testing.T, h *Harness, gsID string, backupID string) {
	t.Helper()
	resp, err := h.PostJSON(
		fmt.Sprintf("/api/gameservers/%s/backups/%s/restore", gsID, backupID), nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.Less(t, resp.StatusCode, 400, "restore should succeed")
}

// collectAllPorts gathers all assigned host ports across multiple gameservers.
func collectAllPorts(t *testing.T, h *Harness, gsIDs []string) []int {
	t.Helper()
	type portEntry struct {
		HostPort int `json:"host_port"`
	}
	var allPorts []int
	for _, id := range gsIDs {
		resp, err := h.Get("/api/gameservers/" + id)
		require.NoError(t, err)
		var gs struct {
			Ports []portEntry `json:"ports"`
		}
		require.NoError(t, DecodeData(resp, &gs))
		for _, p := range gs.Ports {
			allPorts = append(allPorts, p.HostPort)
		}
	}
	return allPorts
}

// waitForStatusOneOf waits until the gameserver reaches any of the given statuses.
func waitForStatusOneOf(h *Harness, gsID string, statuses []string, timeout time.Duration) (string, error) {
	target := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		target[s] = true
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.Get("/api/gameservers/" + gsID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var gs struct{ Status string `json:"status"` }
		if err := DecodeData(resp, &gs); err == nil && target[gs.Status] {
			return gs.Status, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return "", fmt.Errorf("timed out waiting for gameserver %s to reach one of %v", gsID, statuses)
}

// testGameEnv returns the game env from the harness with optional overrides.
// Uses h.GameEnv() as the base so it works for both test-game and real games.
func testGameEnv(h *Harness, overrides map[string]string) map[string]string {
	env := make(map[string]string)
	for k, v := range h.GameEnv() {
		env[k] = v
	}
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

// skipIfNotTestGame skips the test when running against a real game
// that doesn't support TEST_BEHAVIOR injection.
func skipIfNotTestGame(t *testing.T, h *Harness) {
	t.Helper()
	if h.GameID() != "test-game" {
		t.Skipf("test requires test-game (TEST_BEHAVIOR injection), got %s", h.GameID())
	}
}
