//go:build e2e

package e2e

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_ParallelStarts_NoDuplicatePorts creates multiple gameservers and
// starts them concurrently. Verifies that port allocation produces no
// duplicates under contention.
func TestE2E_ParallelStarts_NoDuplicatePorts(t *testing.T) {
	h := Start(t)

	const count = 5
	var gsIDs []string
	for i := 0; i < count; i++ {
		gs := createGameserver(t, h, testGameEnv(h, nil))
		gsIDs = append(gsIDs, gs.ID)
	}

	// Start all concurrently
	var wg sync.WaitGroup
	for _, id := range gsIDs {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			resp, err := h.PostJSON("/api/gameservers/"+id+"/actions/start", nil)
			if err == nil {
				resp.Body.Close()
			}
		}(id)
	}
	wg.Wait()

	// Wait for all to reach running
	for _, id := range gsIDs {
		require.NoError(t, h.WaitForStatus(id, "running", 2*time.Minute),
			"all gameservers should reach running")
	}

	// Verify all host ports are unique
	allPorts := collectAllPorts(t, h, gsIDs)
	seen := make(map[int]bool)
	for _, p := range allPorts {
		assert.False(t, seen[p], "port %d assigned to multiple gameservers", p)
		seen[p] = true
	}
	t.Logf("started %d gameservers concurrently with %d unique ports", count, len(seen))
}

// TestE2E_DoubleStart_Noop verifies that calling start on an already-running
// gameserver is a safe no-op and does not error or disrupt the server.
func TestE2E_DoubleStart_Noop(t *testing.T) {
	h := Start(t)

	gs := createGameserver(t, h, testGameEnv(h, nil))
	startAndWaitRunning(t, h, gs.ID)

	// Second start should not error
	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/actions/start", nil)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Less(t, resp.StatusCode, 500, "double start should not 500")

	// Should still be running
	status, _ := h.GetGameserver(t, gs.ID)
	assert.Equal(t, "running", status, "should still be running after double start")
}

// TestE2E_DoubleStop_Noop verifies that calling stop on an already-stopped
// gameserver is a safe no-op and does not error.
func TestE2E_DoubleStop_Noop(t *testing.T) {
	h := Start(t)

	gs := createGameserver(t, h, testGameEnv(h, nil))
	startAndWaitRunning(t, h, gs.ID)
	stopAndWaitStopped(t, h, gs.ID)

	// Second stop should not error
	resp, err := h.PostJSON("/api/gameservers/"+gs.ID+"/actions/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Less(t, resp.StatusCode, 500, "double stop should not 500")

	// Should still be stopped
	status, _ := h.GetGameserver(t, gs.ID)
	assert.Equal(t, "stopped", status, "should still be stopped after double stop")
}
