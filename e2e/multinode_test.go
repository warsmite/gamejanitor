//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiNode_WorkerConnectAndLifecycle starts a controller-only node and a
// separate worker-only node, then exercises the full multi-node path:
// registration, placement, gameserver lifecycle, worker disconnect, and recovery.
func TestMultiNode_WorkerConnectAndLifecycle(t *testing.T) {
	binary := buildBinary(t)
	runtime := os.Getenv("E2E_RUNTIME")

	// --- Controller ---
	controllerData := t.TempDir()
	copyTestGame(t, controllerData)
	httpPort := freePort(t)
	grpcPort := freePort(t)

	controllerArgs := []string{"serve",
		"--bind", "127.0.0.1",
		"--port", fmt.Sprintf("%d", httpPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--sftp-port", "0",
		"--data-dir", controllerData,
		"--controller",
		"--worker=false",
	}
	controllerCmd := exec.Command(binary, controllerArgs...)
	controllerCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if os.Getenv("E2E_DEBUG") != "" {
		controllerCmd.Stdout = os.Stdout
		controllerCmd.Stderr = os.Stderr
	} else {
		controllerCmd.Stdout = io.Discard
		controllerCmd.Stderr = io.Discard
	}

	t.Logf("starting controller: http=%d grpc=%d data=%s", httpPort, grpcPort, controllerData)
	require.NoError(t, controllerCmd.Start())
	t.Cleanup(func() {
		controllerCmd.Process.Signal(os.Interrupt)
		controllerCmd.Wait()
		cleanupContainers(t)
	})

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	waitForHealth(t, baseURL, 30*time.Second)

	// --- Create worker token via API ---
	workerToken := createWorkerToken(t, baseURL, "test-worker")
	t.Logf("worker token created")

	// --- Worker ---
	workerData := t.TempDir()
	copyTestGame(t, workerData)
	workerGRPCPort := freePort(t)

	workerArgs := []string{"serve",
		"--bind", "0.0.0.0",
		"--controller=false",
		"--worker",
		"--grpc-port", fmt.Sprintf("%d", workerGRPCPort),
		"--sftp-port", "0",
		"--data-dir", workerData,
		"--controller-address", fmt.Sprintf("127.0.0.1:%d", grpcPort),
		"--worker-token", workerToken,
	}
	if runtime != "" {
		workerArgs = append(workerArgs, "--runtime", runtime)
	}

	workerCmd := exec.Command(binary, workerArgs...)
	workerCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if os.Getenv("E2E_DEBUG") != "" {
		workerCmd.Stdout = os.Stdout
		workerCmd.Stderr = os.Stderr
	} else {
		workerCmd.Stdout = io.Discard
		workerCmd.Stderr = io.Discard
	}

	t.Logf("starting worker: grpc=%d data=%s controller=127.0.0.1:%d", workerGRPCPort, workerData, grpcPort)
	require.NoError(t, workerCmd.Start())
	t.Cleanup(func() {
		workerCmd.Process.Signal(os.Interrupt)
		workerCmd.Wait()
	})

	// --- Wait for worker to appear online ---
	workerID := waitForWorkerOnline(t, baseURL, 30*time.Second)
	t.Logf("worker online: %s", workerID)

	// --- Create gameserver ---
	resp := postJSON(t, baseURL+"/api/gameservers", map[string]any{
		"name":    "Multi-Node Test",
		"game_id": "test-game",
		"env":     map[string]string{"REQUIRED_VAR": "yes"},
	})
	var gs struct {
		ID     string `json:"id"`
		NodeID string `json:"node_id"`
	}
	require.NoError(t, decodeData(resp, &gs))
	require.NotEmpty(t, gs.ID)
	t.Logf("gameserver created: %s on node %s", gs.ID, gs.NodeID)

	// --- Start gameserver ---
	resp = postJSON(t, baseURL+"/api/gameservers/"+gs.ID+"/start", nil)
	resp.Body.Close()

	require.NoError(t, waitForStatus(t, baseURL, gs.ID, "running", 60*time.Second),
		"gameserver should reach running on remote worker")
	t.Logf("gameserver running on remote worker")

	// --- Stop gameserver ---
	resp = postJSON(t, baseURL+"/api/gameservers/"+gs.ID+"/stop", nil)
	resp.Body.Close()

	require.NoError(t, waitForStatus(t, baseURL, gs.ID, "stopped", 30*time.Second))
	t.Logf("gameserver stopped")

	// --- Kill worker (simulate crash) ---
	t.Logf("killing worker to simulate disconnect")
	workerCmd.Process.Kill()
	workerCmd.Wait()

	// Wait for reaper to mark worker offline (~30s heartbeat timeout)
	// but the gameserver should become unreachable
	require.NoError(t, waitForWorkerStatus(t, baseURL, workerID, "offline", 45*time.Second),
		"worker should go offline after kill")
	t.Logf("worker offline")

	// --- Restart worker ---
	t.Logf("restarting worker")
	workerCmd = exec.Command(binary, workerArgs...)
	workerCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if os.Getenv("E2E_DEBUG") != "" {
		workerCmd.Stdout = os.Stdout
		workerCmd.Stderr = os.Stderr
	} else {
		workerCmd.Stdout = io.Discard
		workerCmd.Stderr = io.Discard
	}
	require.NoError(t, workerCmd.Start())
	t.Cleanup(func() {
		workerCmd.Process.Signal(os.Interrupt)
		workerCmd.Wait()
	})

	// Worker should reconnect and go back online
	require.NoError(t, waitForWorkerStatus(t, baseURL, workerID, "online", 30*time.Second),
		"worker should reconnect after restart")
	t.Logf("worker back online")

	// --- Cleanup ---
	req, _ := http.NewRequest("DELETE", baseURL+"/api/gameservers/"+gs.ID, nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	delResp.Body.Close()
	assert.Equal(t, 202, delResp.StatusCode)
	t.Logf("gameserver deleted")
}

// --- Helpers ---

func waitForHealth(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("controller did not become ready within %s at %s", timeout, baseURL)
}

func createWorkerToken(t *testing.T, baseURL string, name string) string {
	t.Helper()
	resp := postJSON(t, baseURL+"/api/tokens", map[string]any{
		"name":  name,
		"scope": "worker",
	})
	var result struct {
		Token string `json:"token"`
	}
	require.NoError(t, decodeData(resp, &result))
	require.NotEmpty(t, result.Token)
	return result.Token
}

func waitForWorkerOnline(t *testing.T, baseURL string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/workers")
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var workers []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := decodeData(resp, &workers); err == nil {
			for _, w := range workers {
				if w.Status == "online" {
					return w.ID
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("no worker came online within %s", timeout)
	return ""
}

func waitForWorkerStatus(t *testing.T, baseURL string, workerID string, targetStatus string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/workers")
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var workers []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err := decodeData(resp, &workers); err == nil {
			for _, w := range workers {
				if w.ID == workerID && w.Status == targetStatus {
					return nil
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("worker %s did not reach status %q within %s", workerID, targetStatus, timeout)
}

func waitForStatus(t *testing.T, baseURL string, gsID string, targetStatus string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/api/gameservers/" + gsID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var gs struct {
			Status string `json:"status"`
		}
		if err := decodeData(resp, &gs); err == nil && gs.Status == targetStatus {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("gameserver %s did not reach status %q within %s", gsID, targetStatus, timeout)
}

func postJSON(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	resp, err := http.Post(url, "application/json", reader)
	require.NoError(t, err)
	return resp
}

func decodeData(resp *http.Response, v any) error {
	defer resp.Body.Close()
	var env struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	if env.Status != "ok" {
		return fmt.Errorf("API error: %s", env.Error)
	}
	if v != nil {
		return json.Unmarshal(env.Data, v)
	}
	return nil
}
