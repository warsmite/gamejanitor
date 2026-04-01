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
		localOnce.Do(func() { startLocalInstance(t) })
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

	t.Cleanup(func() {
		if localCmd != nil && localCmd.Process != nil {
			localCmd.Process.Signal(os.Interrupt)
			localCmd.Wait()
		}
		cleanupSandboxState()
		os.RemoveAll(localDir)
	})
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

func (h *Harness) WaitForStatus(gsID, targetStatus string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.Get("/api/gameservers/" + gsID)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		var gs struct{ Status string `json:"status"` }
		if err := DecodeData(resp, &gs); err == nil && gs.Status == targetStatus {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for gameserver %s to reach status %q", gsID, targetStatus)
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
	var files []struct{ Name string `json:"name"` }
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
