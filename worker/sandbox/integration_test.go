//go:build integration

package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/worker"
)

// Integration tests verify real sandbox behavior — process isolation, file
// ownership, signal delivery, volume writes. Requires bwrap on the host.
// Run with: go test -tags integration ./worker/sandbox/

func skipIfNoBwrap(t *testing.T) {
	t.Helper()
	if lookupBinary("bwrap") == "" {
		t.Skip("bwrap not available")
	}
}

func skipIfNotRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root for --uid/--gid")
	}
}

func testLogger() *slog.Logger {
	if os.Getenv("DEBUG_TESTS") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestWorker creates a SandboxWorker with a temp data dir for testing.
func newTestWorker(t *testing.T) *SandboxWorker {
	t.Helper()
	dataDir := t.TempDir()
	log := testLogger()

	paths, err := resolvePaths(dataDir, log)
	require.NoError(t, err)

	w := &SandboxWorker{
		log:       log,
		dataDir:   dataDir,
		paths:     paths,
		instances: make(map[string]*managedInstance),
		eventCh:   make(chan worker.InstanceEvent, 64),
	}
	w.resolve = w.volumeResolver()
	return w
}

// setupHostRootFS creates a rootfs that uses the host / with an entrypoint script.
// Returns the rootfs path and image name.
func setupHostRootFS(t *testing.T, dataDir string, script string) string {
	t.Helper()
	imagesDir := filepath.Join(dataDir, "images", "sha256", "test-image")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, ".extracted"), []byte("test"), 0644)

	// Write image config — use host rootfs, run script via sh
	shPath := lookupBinary("sh")
	require.NotEmpty(t, shPath)

	cfg := fmt.Sprintf(`{"Entrypoint":["%s","-c"],"Cmd":[%q],"Env":["PATH=/usr/bin:/bin:/usr/sbin:/sbin:/run/current-system/sw/bin"],"WorkingDir":"/data"}`, shPath, script)
	os.WriteFile(filepath.Join(imagesDir, ".config.json"), []byte(cfg), 0644)

	// Write index
	indexPath := filepath.Join(dataDir, "images", "index.json")
	os.WriteFile(indexPath, []byte(`{"test:latest":"sha256/test-image"}`), 0644)

	// Create mount points in the rootfs that bwrap expects
	for _, dir := range []string{"data", "scripts", "defaults"} {
		os.MkdirAll(filepath.Join(imagesDir, dir), 0755)
	}

	return imagesDir
}

func TestIntegration_ProcessCanWriteToVolume(t *testing.T) {
	skipIfNoBwrap(t)
	skipIfNotRoot(t)

	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, "mkdir -p /data/.gamejanitor/logs && echo hello > /data/test.txt && echo done")

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "write-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id))

	// Wait for process to finish
	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit in time")
	}

	assert.Equal(t, 0, inst.exitCode, "process should exit cleanly")

	// Verify the file was written to the volume
	volPath := filepath.Join(w.dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "test.txt"))
	require.NoError(t, err)
	assert.Equal(t, "hello\n", string(content))

	// Verify nested directory was created
	assert.DirExists(t, filepath.Join(volPath, ".gamejanitor", "logs"))
}

func TestIntegration_RootfsIsReadOnly(t *testing.T) {
	skipIfNoBwrap(t)
	skipIfNotRoot(t)

	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, "touch /etc/should-fail 2>/dev/null && echo WRITABLE || echo READONLY")

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "ro-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id))

	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit in time")
	}

	logData, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
	assert.Contains(t, string(logData), "READONLY", "rootfs should be read-only")
	assert.NotContains(t, string(logData), "WRITABLE")
}

func TestIntegration_ProcessRunsAsCorrectUID(t *testing.T) {
	skipIfNoBwrap(t)
	skipIfNotRoot(t)

	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	// Write a rootfs with a passwd file containing the gameserver user
	rootFS := setupHostRootFS(t, w.dataDir, "id -u > /data/uid.txt")
	os.MkdirAll(filepath.Join(rootFS, "etc"), 0755)
	os.WriteFile(filepath.Join(rootFS, "etc/passwd"),
		[]byte("root:x:0:0:root:/root:/bin/sh\ngameserver:x:1001:1001::/home/gameserver:/bin/sh\n"), 0644)

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "uid-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id))

	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit in time")
	}

	// Image config has no User field → runs as root (UID 0)
	volPath := filepath.Join(w.dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "uid.txt"))
	require.NoError(t, err)
	assert.Equal(t, "0\n", string(content), "default should run as root when no User in image config")
}

func TestIntegration_StopDeliversSignal(t *testing.T) {
	skipIfNoBwrap(t)
	skipIfNotRoot(t)

	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, `
trap 'echo SIGTERM_RECEIVED > /data/signal.txt; exit 0' TERM
echo ready
while true; do sleep 1; done
`)

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "stop-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id))

	// Wait for "ready" in logs
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
		return strings.Contains(string(data), "ready")
	}, 5*time.Second, 100*time.Millisecond, "process should print ready")

	// Stop with 5s timeout
	require.NoError(t, w.StopInstance(ctx, id, 5))

	// Verify SIGTERM was received
	volPath := filepath.Join(w.dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "signal.txt"))
	require.NoError(t, err)
	assert.Equal(t, "SIGTERM_RECEIVED\n", string(content))
}

func TestIntegration_NetworkNamespaceSetup(t *testing.T) {
	skipIfNoBwrap(t)

	dataDir := t.TempDir()
	log := testLogger()
	paths, err := resolvePaths(dataDir, log)
	require.NoError(t, err)

	if !paths.hasNetworkIsolation() {
		t.Skip("slirp4netns not available")
	}

	os.MkdirAll(filepath.Join(dataDir, "instances", "test-ns"), 0755)

	si, err := setupNetworkNamespace("test-ns", nil, dataDir, paths, log)
	require.NoError(t, err)
	require.NotNil(t, si)
	assert.Greater(t, si.nsPID, 0)

	stopSlirp(si, log)
}
