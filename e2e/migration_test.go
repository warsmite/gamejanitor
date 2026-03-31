//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestE2E_Migration_RunningServer_MigratesAndAutoStarts(t *testing.T) {
	h := Start(t)
	workers := h.Workers(t)
	if len(workers) < 2 {
		t.Skip("need 2+ workers for migration test")
	}

	gameID := h.GameID()
	if gameID == "" {
		t.Skip("set E2E_GAME_ID for remote cluster tests")
	}

	// Create on first worker
	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":            "Migration E2E",
		"game_id":         gameID,
		"memory_limit_mb": 2048,
		"env":             h.GameEnv(),
	})
	require.NoError(t, err)

	var gs struct {
		ID     string `json:"id"`
		NodeID string `json:"node_id"`
	}
	require.NoError(t, DecodeData(resp, &gs))
	require.NotEmpty(t, gs.ID)
	sourceNode := gs.NodeID
	t.Logf("created %s on %s", gs.ID, sourceNode)

	// Cleanup on exit
	t.Cleanup(func() {
		h.Delete("/api/gameservers/" + gs.ID)
	})

	// Start and wait for running
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	require.NoError(t, h.WaitForStatus(gs.ID, "running", 3*time.Minute),
		"gameserver should reach running")
	t.Logf("running on %s", sourceNode)

	// Verify files exist before migration
	files := h.ListFiles(t, gs.ID, "/data")
	require.NotEmpty(t, files, "should have files before migration")
	t.Logf("files before migration: %d", len(files))

	// Pick a different worker
	var targetNode string
	for _, w := range workers {
		if w != sourceNode {
			targetNode = w
			break
		}
	}
	require.NotEmpty(t, targetNode, "should have a different worker to migrate to")

	// Migrate
	t.Logf("migrating from %s to %s", sourceNode, targetNode)
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/migrate", map[string]string{
		"node_id": targetNode,
	})
	require.NoError(t, err)
	resp.Body.Close()

	// Wait for node assignment to change (migration transfer complete)
	require.NoError(t, h.WaitForNodeChange(gs.ID, targetNode, 3*time.Minute),
		"gameserver should move to target node")
	t.Logf("migrated to %s", targetNode)

	// Then wait for auto-start on target
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 3*time.Minute),
		"gameserver should auto-start on target after migration")

	_, newNode := h.GetGameserver(t, gs.ID)
	assert.Equal(t, targetNode, newNode, "should be on target node")
	t.Logf("running on %s after migration", newNode)

	// Verify files survived
	filesAfter := h.ListFiles(t, gs.ID, "/data")
	require.NotEmpty(t, filesAfter, "should have files after migration")
	t.Logf("files after migration: %d", len(filesAfter))
	assert.GreaterOrEqual(t, len(filesAfter), len(files)-1,
		"file count should be similar after migration")
}

func TestE2E_Migration_StoppedServer_StaysStopped(t *testing.T) {
	h := Start(t)
	workers := h.Workers(t)
	if len(workers) < 2 {
		t.Skip("need 2+ workers for migration test")
	}

	gameID := h.GameID()
	if gameID == "" {
		t.Skip("set E2E_GAME_ID for remote cluster tests")
	}

	// Create, start, then stop
	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":            "Migration Stopped E2E",
		"game_id":         gameID,
		"memory_limit_mb": 2048,
		"env":             h.GameEnv(),
	})
	require.NoError(t, err)

	var gs struct {
		ID     string `json:"id"`
		NodeID string `json:"node_id"`
	}
	require.NoError(t, DecodeData(resp, &gs))
	sourceNode := gs.NodeID

	t.Cleanup(func() {
		h.Delete("/api/gameservers/" + gs.ID)
	})

	// Start then stop — so we have files on disk
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.NoError(t, h.WaitForStatus(gs.ID, "running", 3*time.Minute))

	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()
	require.NoError(t, h.WaitForStatus(gs.ID, "stopped", time.Minute))

	// Migrate while stopped
	var targetNode string
	for _, w := range workers {
		if w != sourceNode {
			targetNode = w
			break
		}
	}

	t.Logf("migrating stopped server from %s to %s", sourceNode, targetNode)
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/migrate", map[string]string{
		"node_id": targetNode,
	})
	require.NoError(t, err)
	resp.Body.Close()

	// Wait for node change, should stay stopped
	require.NoError(t, h.WaitForNodeChange(gs.ID, targetNode, 3*time.Minute))

	status, newNode := h.GetGameserver(t, gs.ID)
	assert.Equal(t, targetNode, newNode)
	assert.Equal(t, "stopped", status, "should stay stopped after migrating a stopped server")
	t.Logf("migrated and stayed stopped on %s", newNode)
}
