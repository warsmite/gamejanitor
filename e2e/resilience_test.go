//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_SlowReady_EventuallyRunning verifies that a gameserver with a delayed
// ready pattern sits in "started" during the delay and eventually reaches "running".
func TestE2E_SlowReady_EventuallyRunning(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR":      "slow-ready",
		"READY_DELAY_SECONDS": "8",
	}))

	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// During the delay, status should be "started" (installed but not yet ready)
	// Wait a moment for install to complete, then check intermediate status
	time.Sleep(4 * time.Second)
	status, _ := h.GetGameserver(t, gs.ID)
	assert.Contains(t, []string{"installing", "starting", "started"}, status,
		"should be in a pre-ready state during delay, got %q", status)
	t.Logf("intermediate status during slow ready: %s", status)

	// Should eventually reach running after the delay
	require.NoError(t, h.WaitForStatus(gs.ID, "running", time.Minute),
		"gameserver should reach running after ready delay")
}

// TestE2E_LogFlood_DoesNotOOM verifies that a gameserver flooding stdout does
// not crash gamejanitor and can still be stopped cleanly.
func TestE2E_LogFlood_DoesNotOOM(t *testing.T) {
	h := Start(t)
	skipIfNotTestGame(t, h)

	gs := createGameserver(t, h, testGameEnv(h, map[string]string{
		"TEST_BEHAVIOR": "stdout-flood",
	}))
	startAndWaitRunning(t, h, gs.ID)

	// Let it flood for a while
	t.Logf("letting stdout flood run for 10 seconds...")
	time.Sleep(10 * time.Second)

	// Verify gamejanitor is still responsive and the server is tracked
	status, _ := h.GetGameserver(t, gs.ID)
	assert.Equal(t, "running", status, "gameserver should survive log flood")

	// Stop should still work under log pressure
	stopAndWaitStopped(t, h, gs.ID)
	t.Logf("gameserver stopped cleanly despite log flood")
}

// TestE2E_Backup_RunningServer_RestoreVerifiesData verifies the full backup
// round-trip: write a marker file, create backup while running, restore to a
// new gameserver, and verify the marker file survived.
func TestE2E_Backup_RunningServer_RestoreVerifiesData(t *testing.T) {
	h := Start(t)

	// Create and start the source gameserver
	gs1 := createGameserver(t, h, testGameEnv(h, nil))
	startAndWaitRunning(t, h, gs1.ID)

	// Write a unique marker file
	marker := "backup-round-trip-" + gs1.ID
	writeFile(t, h, gs1.ID, "/data/backup-marker.txt", marker)

	// Create backup while running
	backup := createBackup(t, h, gs1.ID)
	t.Logf("backup created: %s", backup.ID)
	waitForBackupComplete(t, h, gs1.ID, backup.ID)
	t.Logf("backup completed")

	// Stop source
	stopAndWaitStopped(t, h, gs1.ID)

	// Create a new gameserver and restore the backup into it
	gs2 := createGameserver(t, h, testGameEnv(h, nil))
	restoreBackup(t, h, gs2.ID, backup.ID)

	// Give the restore a moment to complete
	time.Sleep(3 * time.Second)

	// Verify the marker file survived the round-trip
	content := readFile(t, h, gs2.ID, "/data/backup-marker.txt")
	assert.Contains(t, content, marker,
		"marker file should survive backup/restore round-trip")
	t.Logf("backup round-trip verified: marker file intact")
}
