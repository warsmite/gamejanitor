//go:build integration

package local

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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
// Run with: go test -tags integration ./worker/local/

func skipIfNoBwrap(t *testing.T) {
	t.Helper()
	if lookupBinary("bwrap") == "" {
		t.Skip("bwrap not available")
	}
}


func testLogger() *slog.Logger {
	if os.Getenv("DEBUG_TESTS") != "" {
		return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// newTestWorker creates a LocalWorker with a temp data dir for testing.
func newTestWorker(t *testing.T) *LocalWorker {
	t.Helper()
	dataDir := t.TempDir()
	log := testLogger()

	paths, err := resolvePaths(dataDir, log)
	require.NoError(t, err)

	w := &LocalWorker{
		log:       log,
		dataDir:   dataDir,
		paths:     paths,
		instances: make(map[string]*managedInstance),
		tracker:   NewInstanceTracker(log),
	}
	w.resolve = w.volumeResolver()
	return w
}

// setupHostRootFS creates a rootfs that uses the host / with an entrypoint script.
// Returns the rootfs path and image name.
// setupHostRootFS registers a test image that uses the host / as its rootfs.
// Returns the rootfs path. Since bwrap needs mountpoints to exist in the
// rootfs for --dev, --proc, --tmpfs, etc., and we can't create them in /,
// we use a tmpfs overlay: the rootfs dir contains only the mountpoint stubs,
// and the host / is added as a bind mount so all host binaries are accessible.
//
// The trick: we set the rootfs to a small stub directory (for .extracted and
// .config.json), but add "/" as a read-only bind in the manifest so bwrap
// sees the full host filesystem.
func setupHostRootFS(t *testing.T, dataDir string, script string) string {
	t.Helper()
	rootFS := filepath.Join(dataDir, "images", "sha256", "test-image")
	os.MkdirAll(rootFS, 0755)
	os.WriteFile(filepath.Join(rootFS, ".extracted"), []byte("test"), 0644)

	shPath := lookupBinary("sh")
	require.NotEmpty(t, shPath)

	cfg := fmt.Sprintf(`{"Entrypoint":["%s","-c"],"Cmd":[%q],"Env":["PATH=/usr/bin:/bin:/usr/sbin:/sbin:/run/current-system/sw/bin"],"WorkingDir":"/data"}`, shPath, script)
	os.WriteFile(filepath.Join(rootFS, ".config.json"), []byte(cfg), 0644)

	indexPath := filepath.Join(dataDir, "images", "index.json")
	os.WriteFile(indexPath, []byte(`{"test:latest":"sha256/test-image"}`), 0644)

	// Create all mount points that bwrap needs for --dev, --proc, --tmpfs, --ro-bind
	for _, dir := range []string{
		"data", "scripts", "defaults",
		"dev", "proc", "tmp", "home", "run", "run/current-system", "var/tmp",
		"etc", "etc/ssl/certs", "etc/pki/tls/certs",
	} {
		os.MkdirAll(filepath.Join(rootFS, dir), 0755)
	}
	os.WriteFile(filepath.Join(rootFS, "etc/resolv.conf"), nil, 0644)

	// Bind-mount host directories containing binaries and libraries so the
	// test entrypoint can find sh, coreutils, etc. In production, OCI images
	// are complete rootfs and don't need this.
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64", "/nix", "/sbin"} {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			os.MkdirAll(filepath.Join(rootFS, dir), 0755)
		}
	}

	return rootFS
}

// testHostBinds returns read-only bind mounts for host binary directories.
// Call after setupHostRootFS (which creates the mountpoint dirs).
func testHostBinds() []string {
	var binds []string
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64", "/nix", "/sbin", "/run/current-system"} {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			binds = append(binds, dir+":"+dir+":ro")
		}
	}
	return binds
}

func TestIntegration_ProcessCanWriteToVolume(t *testing.T) {
	skipIfNoBwrap(t)


	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, "mkdir -p /data/.gamejanitor/logs && echo hello > /data/test.txt && echo done")

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "write-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

	// Wait for process to finish
	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit in time")
	}

	assert.Equal(t, int32(0), inst.exitCode.Load(), "process should exit cleanly")

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


	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, "touch /etc/should-fail 2>/dev/null && echo WRITABLE || echo READONLY")

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "ro-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

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
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

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
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

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

func TestIntegration_ExecInRunningInstance(t *testing.T) {
	skipIfNoBwrap(t)


	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))

	// Create a rootfs with a /scripts/send-command script (mimics real game setup)
	rootFS := setupHostRootFS(t, w.dataDir, `
trap 'exit 0' TERM
echo ready
while true; do sleep 1; done
`)
	scriptsDir := filepath.Join(rootFS, "scripts")
	os.MkdirAll(scriptsDir, 0755)
	os.WriteFile(filepath.Join(scriptsDir, "send-command"), []byte("#!/bin/sh\necho \"command: $1\"\n"), 0755)

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "exec-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      append(testHostBinds(), scriptsDir+":/scripts:ro"),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

	// Wait for process to be ready
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
		return strings.Contains(string(data), "ready")
	}, 5*time.Second, 100*time.Millisecond, "process should print ready")

	// Exec send-command inside the running sandbox
	exitCode, stdout, stderr, err := w.Exec(ctx, id, []string{"/scripts/send-command", "test-input"})
	require.NoError(t, err, "Exec should not return an error; stderr: %s", stderr)
	assert.Equal(t, 0, exitCode, "send-command should exit 0; stderr: %s", stderr)
	assert.Contains(t, stdout, "command: test-input")

	// Verify env vars from the sandbox are inherited (bwrap --setenv sets PATH)
	exitCode, stdout, _, err = w.Exec(ctx, id, []string{"/bin/sh", "-c", "echo $PATH"})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.NotEmpty(t, strings.TrimSpace(stdout), "PATH should be inherited from sandbox env")

	require.NoError(t, w.StopInstance(ctx, id, 5))
}

func TestIntegration_InstanceSurvivesWorkerRestart(t *testing.T) {
	skipIfNoBwrap(t)

	dataDir := t.TempDir()

	// Worker 1: start a long-running instance
	w1 := newTestWorkerWithDir(t, dataDir)
	ctx := context.Background()

	require.NoError(t, w1.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, dataDir, `
trap 'echo STOPPED > /data/status.txt; exit 0' TERM
echo RUNNING > /data/status.txt
echo ready
while true; do sleep 1; done
`)

	id, err := w1.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "survive-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w1.StartInstance(ctx, id, ""))

	// Wait for ready
	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(w1.instanceDir(id), "output.log"))
		return strings.Contains(string(data), "ready")
	}, 5*time.Second, 100*time.Millisecond, "instance should print ready")

	// Verify state.json was persisted
	stateFile := filepath.Join(dataDir, "instances", id, "state.json")
	assert.FileExists(t, stateFile)

	info, err := w1.InspectInstance(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "running", info.State)

	// Discard worker 1 (simulates gamejanitor exit — process survives because
	// we removed --die-with-parent)
	w1 = nil

	// Worker 2: create from same dataDir — recoverInstances should find it
	w2 := newTestWorkerWithRecovery(t, dataDir)

	// Verify the instance was recovered
	info, err = w2.InspectInstance(ctx, id)
	require.NoError(t, err, "recovered worker should find the instance")
	assert.Equal(t, "running", info.State)

	// Verify the process is still actually running (wrote RUNNING to volume)
	volPath := filepath.Join(dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "status.txt"))
	require.NoError(t, err)
	assert.Equal(t, "RUNNING\n", string(content))

	// Stop the recovered instance — verify stop works on re-adopted processes
	require.NoError(t, w2.StopInstance(ctx, id, 5))

	w2.mu.Lock()
	inst := w2.instances[id]
	w2.mu.Unlock()

	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("recovered instance did not exit after stop")
	}

	// Verify clean shutdown (SIGTERM handler wrote STOPPED)
	content, err = os.ReadFile(filepath.Join(volPath, "status.txt"))
	require.NoError(t, err)
	assert.Equal(t, "STOPPED\n", string(content))

	// state.json should be cleaned up
	assert.NoFileExists(t, stateFile)
}

func TestIntegration_RecoverySkipsExitedInstances(t *testing.T) {
	skipIfNoBwrap(t)

	dataDir := t.TempDir()

	// Worker 1: start a short-lived instance that exits immediately
	w1 := newTestWorkerWithDir(t, dataDir)
	ctx := context.Background()

	require.NoError(t, w1.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, dataDir, "echo done")

	id, err := w1.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "exit-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w1.StartInstance(ctx, id, ""))

	// Wait for it to exit
	w1.mu.Lock()
	inst := w1.instances[id]
	w1.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit")
	}

	// state.json should have been cleaned up by exit watcher
	stateFile := filepath.Join(dataDir, "instances", id, "state.json")
	assert.NoFileExists(t, stateFile)

	// Worker 2: should NOT recover this exited instance
	w2 := newTestWorkerWithRecovery(t, dataDir)
	_, err = w2.InspectInstance(ctx, id)
	assert.Error(t, err, "exited instance should not be found after recovery")
}

func TestIntegration_StatePersistence(t *testing.T) {
	skipIfNoBwrap(t)

	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, `
trap 'exit 0' TERM
echo ready
while true; do sleep 1; done
`)

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "state-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

	require.Eventually(t, func() bool {
		data, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
		return strings.Contains(string(data), "ready")
	}, 5*time.Second, 100*time.Millisecond)

	// Verify state.json contents
	dir := filepath.Join(w.dataDir, "instances", id)
	state, err := loadInstanceState(dir)
	require.NoError(t, err)
	assert.Equal(t, "gj-"+id, state.UnitName)
	assert.False(t, state.StartedAt.IsZero())

	// Stop and verify state.json is cleaned up
	require.NoError(t, w.StopInstance(ctx, id, 5))

	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(10 * time.Second):
		t.Fatal("instance did not exit")
	}

	assert.NoFileExists(t, filepath.Join(dir, "state.json"))
}

// newTestWorkerWithDir creates a LocalWorker with a specific data dir (no recovery).
func newTestWorkerWithDir(t *testing.T, dataDir string) *LocalWorker {
	t.Helper()
	log := testLogger()

	paths, err := resolvePaths(dataDir, log)
	require.NoError(t, err)

	w := &LocalWorker{
		log:       log,
		dataDir:   dataDir,
		paths:     paths,
		instances: make(map[string]*managedInstance),
		tracker:   NewInstanceTracker(log),
	}
	w.resolve = w.volumeResolver()
	return w
}

// newTestWorkerWithRecovery creates a LocalWorker that runs recoverInstances,
// simulating a gamejanitor restart against the same dataDir.
func newTestWorkerWithRecovery(t *testing.T, dataDir string) *LocalWorker {
	t.Helper()
	w := newTestWorkerWithDir(t, dataDir)
	w.recoverInstances()
	return w
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
