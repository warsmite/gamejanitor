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
	"testing"
	"time"
)

// Harness manages a real gamejanitor instance for end-to-end testing.
type Harness struct {
	BaseURL  string
	DataDir  string
	Port     int
	GRPCPort int

	cmd    *exec.Cmd
	t      *testing.T
}

// Start returns a harness connected to a gamejanitor instance.
// If GAMEJANITOR_API_URL is set, connects to an existing remote cluster (homelab/CI).
// Otherwise, builds and launches a local instance with test-game.
func Start(t *testing.T) *Harness {
	t.Helper()

	if url := os.Getenv("GAMEJANITOR_API_URL"); url != "" {
		return startRemote(t, url)
	}
	return startLocal(t)
}

// startRemote connects to an existing cluster — no process management.
func startRemote(t *testing.T, baseURL string) *Harness {
	t.Helper()
	h := &Harness{
		BaseURL: baseURL,
		t:       t,
	}
	h.waitForReady(t)
	h.waitForWorker(t)
	t.Logf("connected to remote cluster at %s", baseURL)
	return h
}

// startLocal builds and launches a local gamejanitor instance with test-game.
func startLocal(t *testing.T) *Harness {
	t.Helper()

	dataDir := t.TempDir()
	port := freePort(t)
	grpcPort := freePort(t)
	workerGRPCPort := freePort(t)

	// Copy the test game definition into the data dir so gamejanitor loads it as a local override
	copyTestGame(t, dataDir)

	h := &Harness{
		BaseURL:  fmt.Sprintf("http://127.0.0.1:%d", port),
		DataDir:  dataDir,
		Port:     port,
		GRPCPort: grpcPort,
		t:        t,
	}

	// Build the binary if needed
	binary := buildBinary(t)

	// Use explicit runtime if set via E2E_RUNTIME env, otherwise auto-detect
	runtime := os.Getenv("E2E_RUNTIME")
	args := []string{"serve",
		"--bind", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--worker-grpc-port", fmt.Sprintf("%d", workerGRPCPort),
		"--sftp-port", "0",
		"--data-dir", dataDir,
		"--controller",
		"--worker",
	}
	if runtime != "" {
		args = append(args, "--runtime", runtime)
	}

	h.cmd = exec.Command(binary, args...)
	h.cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	// Capture output for debugging
	if os.Getenv("E2E_DEBUG") != "" {
		h.cmd.Stdout = os.Stdout
		h.cmd.Stderr = os.Stderr
	} else {
		h.cmd.Stdout = io.Discard
		h.cmd.Stderr = io.Discard
	}

	t.Logf("starting gamejanitor: port=%d grpc=%d data=%s", port, grpcPort, dataDir)
	if err := h.cmd.Start(); err != nil {
		t.Fatalf("failed to start gamejanitor: %v", err)
	}

	t.Cleanup(func() {
		h.Stop()
		// Clean up any containers/volumes created during the test
		cleanupContainers(t)
	})

	h.waitForReady(t)
	h.waitForWorker(t)
	return h
}

// IsRemote returns true if connected to an existing cluster (not locally managed).
func (h *Harness) IsRemote() bool {
	return h.cmd == nil
}

// WorkerCount returns the number of online workers.
func (h *Harness) WorkerCount(t *testing.T) int {
	t.Helper()
	resp, err := h.Get("/api/workers")
	if err != nil {
		return 0
	}
	var workers []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := DecodeData(resp, &workers); err != nil {
		return 0
	}
	count := 0
	for _, w := range workers {
		if w.Status == "online" {
			count++
		}
	}
	return count
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

func (h *Harness) Stop() {
	if h.cmd != nil && h.cmd.Process != nil {
		h.cmd.Process.Signal(os.Interrupt)
		h.cmd.Wait()
	}
}

// API helpers

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

// DecodeData reads the API response envelope and unmarshals the data field.
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

// GameID returns the game ID to use for tests. Remote clusters use minecraft-java
// (a real game), local clusters use test-game (lightweight fake).
func (h *Harness) GameID() string {
	if h.IsRemote() {
		return os.Getenv("E2E_GAME_ID")
	}
	return "test-game"
}

// GameEnv returns the env vars needed for the test game.
func (h *Harness) GameEnv() map[string]string {
	if h.IsRemote() {
		// Minecraft needs EULA + version
		return map[string]string{"EULA": "true", "MINECRAFT_VERSION": "1.21.4"}
	}
	return map[string]string{"REQUIRED_VAR": "yes"}
}

// GetGameserver fetches a gameserver by ID.
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

// WaitForNodeChange polls until the gameserver's node_id changes to targetNodeID.
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

// ListFiles returns file names at a path in a gameserver volume.
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

// WaitForStatus polls the gameserver until it reaches the target status or times out.
func (h *Harness) WaitForStatus(gsID, targetStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.Get("/api/gameservers/" + gsID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var gs struct {
			Status string `json:"status"`
		}
		if err := DecodeData(resp, &gs); err == nil && gs.Status == targetStatus {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for gameserver %s to reach status %q", gsID, targetStatus)
}

// Internal

func (h *Harness) waitForReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(h.BaseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				t.Logf("gamejanitor ready at %s", h.BaseURL)
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("gamejanitor did not become ready within 30s at %s", h.BaseURL)
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
			t.Logf("worker available (%d online)", len(workers))
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

// buildBinary compiles the gamejanitor binary once per test run.
var cachedBinary string

func buildBinary(t *testing.T) string {
	t.Helper()
	if cachedBinary != "" {
		return cachedBinary
	}

	projectRoot := projectDir()
	binary := filepath.Join(os.TempDir(), "gamejanitor-e2e")
	cmd := exec.Command("go", "build", "-o", binary, ".")
	cmd.Dir = projectRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building gamejanitor binary: %v\n%s", err, out)
	}
	cachedBinary = binary
	t.Logf("built binary: %s", binary)
	return cachedBinary
}

func projectDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filename))
}

func testGameDir() string {
	return filepath.Join(projectDir(), "testdata", "games", "test-game")
}

func copyTestGame(t *testing.T, dataDir string) {
	t.Helper()
	src := testGameDir()
	dst := filepath.Join(dataDir, "games", "test-game")

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)

		if info.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
	if err != nil {
		t.Fatalf("copying test game to data dir: %v", err)
	}
}

func cleanupContainers(t *testing.T) {
	t.Helper()
	// Best effort cleanup of any gamejanitor- prefixed containers and volumes
	exec.Command("docker", "ps", "-aq", "--filter", "name=gamejanitor-").Output()
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name=gamejanitor-").Output(); len(out) > 0 {
		exec.Command("sh", "-c", "docker rm -f $(docker ps -aq --filter name=gamejanitor-)").Run()
	}
	if out, _ := exec.Command("docker", "volume", "ls", "-q", "--filter", "name=gamejanitor-").Output(); len(out) > 0 {
		exec.Command("sh", "-c", "docker volume rm -f $(docker volume ls -q --filter name=gamejanitor-)").Run()
	}
}
