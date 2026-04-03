//go:build e2e || smoke

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Harness connects to a gamejanitor instance for end-to-end testing.
// Locally: starts one shared gamejanitor process per test suite.
// Remote (GAMEJANITOR_API_URL set): connects to an existing cluster.
type Harness struct {
	BaseURL string
	t       *testing.T
}

// --- Shared local instance (one per test suite) ---

var (
	localOnce sync.Once
	localURL  string
	localCmd  *exec.Cmd
	localDir  string
)

// Start returns a harness. All tests in a suite share one gamejanitor instance.
func Start(t *testing.T) *Harness {
	t.Helper()
	t.Parallel()

	url := os.Getenv("GAMEJANITOR_API_URL")
	if url == "" {
		localOnce.Do(func() {
			startLocalInstance(t)
			warmImage(t)
		})
		url = localURL
	}

	h := &Harness{BaseURL: url, t: t}
	h.waitForReady(t)
	h.waitForWorker(t)
	return h
}

func startLocalInstance(t *testing.T) {
	t.Helper()
	cleanupSandboxState()

	dir, err := os.MkdirTemp("", "gamejanitor-e2e-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	localDir = dir

	copyTestGame(t, dir)

	port := freePort(t)
	grpcPort := freePort(t)
	workerGRPCPort := freePort(t)

	binary := buildBinary(t)
	args := []string{"serve",
		"--bind", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--worker-grpc-port", fmt.Sprintf("%d", workerGRPCPort),
		"--sftp-port", "0",
		"--data-dir", dir,
		"--controller", "--worker",
	}
	if rt := os.Getenv("E2E_RUNTIME"); rt != "" {
		args = append(args, "--runtime", rt)
	}

	localCmd = exec.Command(binary, args...)
	localCmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if os.Getenv("E2E_DEBUG") != "" {
		localCmd.Stdout = os.Stdout
		localCmd.Stderr = os.Stderr
	} else {
		localCmd.Stdout = io.Discard
		localCmd.Stderr = io.Discard
	}

	t.Logf("starting gamejanitor: port=%d grpc=%d data=%s", port, grpcPort, dir)
	if err := localCmd.Start(); err != nil {
		t.Fatalf("failed to start gamejanitor: %v", err)
	}

	localURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// Cleanup is handled by TestMain after all tests finish, not here.
	// Registering cleanup on the first test's t would kill the shared
	// instance when that test finishes — while others still need it.
}

// warmImage pre-pulls the game image by creating, starting, and deleting a
// throwaway gameserver. This ensures the OCI image is cached before parallel
// tests start, avoiding concurrent pull races.
func warmImage(t *testing.T) {
	t.Helper()
	h := &Harness{BaseURL: localURL}

	// Wait for the instance to be ready
	h.waitForReady(t)
	h.waitForWorker(t)

	gameID := "test-game"
	if id := os.Getenv("E2E_GAME_ID"); id != "" {
		gameID = id
	}
	env := map[string]string{"REQUIRED_VAR": "yes"}
	if gameID == "minecraft-java" {
		env = map[string]string{"EULA": "true", "MINECRAFT_VERSION": "1.21.4"}
	}

	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name": "image-warmup", "game_id": gameID, "env": env,
	})
	if err != nil {
		t.Logf("warmImage: create failed: %v", err)
		return
	}
	var gs struct {
		ID string `json:"id"`
	}
	if err := DecodeData(resp, &gs); err != nil {
		t.Logf("warmImage: decode failed: %v", err)
		return
	}

	resp, _ = h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	if resp != nil {
		resp.Body.Close()
	}

	if err := h.WaitForStatus(gs.ID, "running", 2*time.Minute); err != nil {
		t.Logf("warmImage: wait for running failed: %v", err)
	}

	// Stop and delete
	resp, _ = h.PostJSON("/api/gameservers/"+gs.ID+"/stop", nil)
	if resp != nil {
		resp.Body.Close()
	}
	h.WaitForStatus(gs.ID, "stopped", 30*time.Second)
	h.Delete("/api/gameservers/" + gs.ID)

	t.Logf("warmImage: image cached for %s", gameID)
}

// --- Game config ---

// GameID returns the game to test. Defaults to test-game.
func (h *Harness) GameID() string {
	if id := os.Getenv("E2E_GAME_ID"); id != "" {
		return id
	}
	return "test-game"
}

// GameEnv returns env vars for the game.
func (h *Harness) GameEnv() map[string]string {
	switch h.GameID() {
	case "minecraft-java":
		return map[string]string{"EULA": "true", "MINECRAFT_VERSION": "1.21.4"}
	default:
		return map[string]string{"REQUIRED_VAR": "yes"}
	}
}

// --- API helpers ---

func (h *Harness) Get(path string) (*http.Response, error) {
	return http.Get(h.BaseURL + path)
}

func (h *Harness) PostJSON(path string, body any) (*http.Response, error) {
	data, _ := json.Marshal(body)
	return http.Post(h.BaseURL+path, "application/json", bytes.NewReader(data))
}

func (h *Harness) Delete(path string) (*http.Response, error) {
	req, _ := http.NewRequest("DELETE", h.BaseURL+path, nil)
	return http.DefaultClient.Do(req)
}

func (h *Harness) Patch(path string, body any) (*http.Response, error) {
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("PATCH", h.BaseURL+path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(req)
}

func DecodeData(resp *http.Response, v any) error {
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

// --- Status helpers ---

// WaitForStatus polls until the gameserver reaches the target status.
// Fails fast if the status reaches a terminal state that can't transition
// to the target (e.g. waiting for "running" but status becomes "error").
func (h *Harness) WaitForStatus(gsID, targetStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastStatus string
	for time.Now().Before(deadline) {
		resp, err := h.Get("/api/gameservers/" + gsID)
		if err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		var gs struct {
			Status      string `json:"status"`
			ErrorReason string `json:"error_reason"`
		}
		if err := DecodeData(resp, &gs); err != nil {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if gs.Status == targetStatus {
			return nil
		}
		lastStatus = gs.Status

		// Fail fast on terminal states that can't reach the target
		if targetStatus == "running" && (gs.Status == "error" || gs.Status == "stopped") {
			reason := gs.ErrorReason
			if reason == "" {
				reason = "no error reason"
			}
			return fmt.Errorf("gameserver %s reached terminal status %q (wanted %q): %s", gsID, gs.Status, targetStatus, reason)
		}
		if targetStatus == "stopped" && gs.Status == "error" {
			return fmt.Errorf("gameserver %s reached %q instead of %q: %s", gsID, gs.Status, targetStatus, gs.ErrorReason)
		}

		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for gameserver %s to reach status %q (last seen: %q)", gsID, targetStatus, lastStatus)
}

func (h *Harness) GetGameserver(t *testing.T, gsID string) (status string, nodeID string) {
	t.Helper()
	resp, err := h.Get("/api/gameservers/" + gsID)
	if err != nil {
		return "", ""
	}
	var gs struct {
		Status string  `json:"status"`
		NodeID *string `json:"node_id"`
	}
	if err := DecodeData(resp, &gs); err != nil {
		return "", ""
	}
	nid := ""
	if gs.NodeID != nil {
		nid = *gs.NodeID
	}
	return gs.Status, nid
}

func (h *Harness) WaitForNodeChange(gsID, targetNodeID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, nodeID := h.GetGameserver(h.t, gsID)
		if nodeID == targetNodeID {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("timed out waiting for gameserver %s to move to node %s", gsID, targetNodeID)
}

// Workers returns online worker IDs.
func (h *Harness) Workers(t *testing.T) []string {
	t.Helper()
	resp, err := h.Get("/api/workers")
	if err != nil {
		return nil
	}
	var workers []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := DecodeData(resp, &workers); err != nil {
		return nil
	}
	var ids []string
	for _, w := range workers {
		if w.Status == "online" {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

func (h *Harness) ListFiles(t *testing.T, gsID string, path string) []string {
	t.Helper()
	resp, err := h.Get("/api/gameservers/" + gsID + "/files?path=" + path)
	if err != nil {
		return nil
	}
	var files []struct {
		Name string `json:"name"`
	}
	if err := DecodeData(resp, &files); err != nil {
		return nil
	}
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return names
}

// --- Internal ---

func (h *Harness) waitForReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.BaseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("gamejanitor not ready within 30s at %s", h.BaseURL)
}

func (h *Harness) waitForWorker(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.BaseURL + "/api/workers")
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		var workers []json.RawMessage
		if err := DecodeData(resp, &workers); err == nil && len(workers) > 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no workers available within 30s")
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

var cachedBinary string

func buildBinary(t *testing.T) string {
	t.Helper()
	if cachedBinary != "" {
		return cachedBinary
	}
	root := projectDir()
	binary := filepath.Join(os.TempDir(), "gamejanitor-e2e")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building binary: %v\n%s", err, out)
	}
	cachedBinary = binary
	return binary
}

func projectDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filename))
}

func copyTestGame(t *testing.T, dataDir string) {
	t.Helper()
	src := filepath.Join(projectDir(), "testdata", "games", "test-game")
	dst := filepath.Join(dataDir, "games", "test-game")
	filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, _ := os.ReadFile(path)
		return os.WriteFile(target, data, info.Mode())
	})
}

// cleanupSandboxState kills orphaned sandbox processes and resets failed systemd scopes.
func cleanupSandboxState() {
	exec.Command("sh", "-c", "pkill -f 'unshare.*sleep infinity' 2>/dev/null").Run()
	exec.Command("sh", "-c", "pkill -f slirp4netns 2>/dev/null").Run()

	// Reset failed scopes
	for _, prefix := range [][]string{{"--user"}, {}} {
		out, _ := exec.Command("systemctl", append(prefix, "list-units", "--type=scope", "--state=failed", "--no-legend", "--plain")...).Output()
		for _, line := range strings.Split(string(out), "\n") {
			unit := strings.Fields(line)
			if len(unit) > 0 && strings.HasPrefix(unit[0], "gj-") {
				exec.Command("systemctl", append(prefix, "reset-failed", unit[0])...).Run()
			}
		}
	}

	// Docker cleanup
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=gamejanitor-").Output(); len(out) > 0 {
		exec.Command("sh", "-c", "docker rm -f $(docker ps -aq --filter name=gamejanitor-)").Run()
	}

	time.Sleep(300 * time.Millisecond)
}
