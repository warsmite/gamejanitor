//go:build e2e

package e2e

// testinfra.go — process management helpers used to spin up the controller
// (and sometimes a separate worker) for e2e tests. Most tests reach this
// only through Start() / NewEnv(); multi-node tests call the lower-level
// helpers directly to run their own processes.

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	localOnce sync.Once
	localURL  string
	localCmd  *exec.Cmd
	localDir  string
)

// startLocalInstance spins up a controller+worker gamejanitor process in a
// temp dir. Called at most once per test process via localOnce.
func startLocalInstance(t *testing.T) {
	t.Helper()
	cleanupSandboxState()

	dir, err := os.MkdirTemp("", "gamejanitor-e2e-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
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
		t.Fatalf("start gamejanitor: %v", err)
	}

	localURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	// Teardown is done in TestMain after all tests finish; registering a
	// t.Cleanup on the first test would kill the shared instance while
	// later parallel tests still need it.
}

// --- Process-management primitives (shared by Env + multinode test) ---

var cachedBinary string

// buildBinary compiles the gamejanitor binary once per test process.
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
		t.Fatalf("build binary: %v\n%s", err, out)
	}
	cachedBinary = binary
	return binary
}

func projectDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(filename))
}

// copyTestGame mirrors the test-game definition into dataDir so the
// controller picks it up via its custom games path.
func copyTestGame(t *testing.T, dataDir string) {
	t.Helper()
	src := filepath.Join(projectDir(), "testdata", "games", "test-game")
	dst := filepath.Join(dataDir, "games", "test-game")
	_ = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
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

// freePort returns an OS-chosen free TCP port.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}

// cleanupSandboxState pkills orphaned sandbox processes and resets failed
// systemd scopes left by previous runs. Called once at local-instance start.
func cleanupSandboxState() {
	_ = exec.Command("sh", "-c", "pkill -f 'unshare.*sleep infinity' 2>/dev/null").Run()

	for _, prefix := range [][]string{{"--user"}, {}} {
		out, _ := exec.Command("systemctl", append(prefix, "list-units", "--type=scope", "--state=failed", "--no-legend", "--plain")...).Output()
		for _, line := range strings.Split(string(out), "\n") {
			unit := strings.Fields(line)
			if len(unit) > 0 && strings.HasPrefix(unit[0], "gj-") {
				_ = exec.Command("systemctl", append(prefix, "reset-failed", unit[0])...).Run()
			}
		}
	}

	time.Sleep(300 * time.Millisecond)
}
