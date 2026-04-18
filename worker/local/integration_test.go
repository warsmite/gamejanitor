//go:build integration

package local

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// Integration tests verify real container behavior — process isolation, file
// ownership, signal delivery, volume writes. Requires crun (embedded or on host).
// Run with: go test -tags integration ./worker/local/

func TestMain(m *testing.M) {
	// Integration tests need a user namespace for crun to create network
	// namespaces. Re-exec through the userns helper if not already in one.
	if !runtime.InUserNamespace() {
		dataDir, err := os.MkdirTemp("", "userns-helper-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "creating temp dir: %v\n", err)
			os.Exit(1)
		}
		defer os.RemoveAll(dataDir)

		helperPath, err := runtime.ExtractUserns(dataDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "extracting userns helper: %v\n", err)
			os.Exit(1)
		}

		self, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolving self: %v\n", err)
			os.Exit(1)
		}

		argv := append([]string{helperPath, self}, os.Args[1:]...)
		if err := syscall.Exec(helperPath, argv, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "userns re-exec: %v\n", err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
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

	rt, err := runtime.New(dataDir, log)
	require.NoError(t, err, "crun runtime must be available for integration tests")

	w := &LocalWorker{
		log:       log,
		dataDir:   dataDir,
		rt:        rt,
		instances: make(map[string]*managedInstance),
		tracker:   NewInstanceTracker(log),
	}
	w.resolve = w.volumeResolver()
	return w
}

// setupHostRootFS registers a test image that uses the host / as its rootfs.
// Returns the rootfs path. The rootfs dir contains mountpoint stubs and host
// binary directories are added as bind mounts so the test entrypoint can find
// sh, coreutils, etc. In production, OCI images are complete rootfs.
func setupHostRootFS(t *testing.T, dataDir string, script string) string {
	t.Helper()
	rootFS := filepath.Join(dataDir, "images", "sha256", "test-image")
	os.MkdirAll(rootFS, 0755)
	os.WriteFile(filepath.Join(rootFS, ".extracted"), []byte("test"), 0644)

	shPath, err := exec.LookPath("sh")
	require.NoError(t, err, "sh must be on PATH for integration tests")

	cfg := fmt.Sprintf(`{"Entrypoint":["%s","-c"],"Cmd":[%q],"Env":["PATH=/usr/bin:/bin:/usr/sbin:/sbin:/run/current-system/sw/bin"],"WorkingDir":"/data"}`, shPath, script)
	os.WriteFile(filepath.Join(rootFS, ".config.json"), []byte(cfg), 0644)

	indexPath := filepath.Join(dataDir, "images", "index.json")
	os.WriteFile(indexPath, []byte(`{"test:latest":"sha256/test-image"}`), 0644)

	// Create mount points that crun needs for the OCI spec mounts
	for _, dir := range []string{
		"data", "scripts", "defaults",
		"dev", "proc", "tmp", "home", "run", "run/current-system", "var/tmp",
		"etc", "etc/ssl/certs", "etc/pki/tls/certs",
	} {
		os.MkdirAll(filepath.Join(rootFS, dir), 0755)
	}
	os.WriteFile(filepath.Join(rootFS, "etc/resolv.conf"), nil, 0644)

	// Bind-mount host directories containing binaries and libraries so the
	// test entrypoint can find sh, coreutils, etc.
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

func TestIntegration_DNSResolution(t *testing.T) {
	w := newTestWorker(t)
	ctx := context.Background()

	require.NoError(t, w.CreateVolume(ctx, "test-vol"))
	setupHostRootFS(t, w.dataDir, "curl -s --max-time 10 -o /dev/null -w '%{http_code}' http://httpbin.org/get > /data/result.txt 2>&1")

	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "dns-test",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Binds:      testHostBinds(),
		Ports: []worker.PortBinding{
			{Port: 30000, ContainerPort: 30000, Protocol: "tcp"},
		},
	})
	require.NoError(t, err)
	require.NoError(t, w.StartInstance(ctx, id, ""))

	w.mu.Lock()
	inst := w.instances[id]
	w.mu.Unlock()
	select {
	case <-inst.done:
	case <-time.After(30 * time.Second):
		t.Fatal("instance did not exit in time")
	}

	volPath := filepath.Join(w.dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "result.txt"))
	require.NoError(t, err)
	result := strings.TrimSpace(string(content))
	t.Logf("curl response: %s", result)
	assert.Equal(t, "200", result, "DNS resolution and HTTPS should work inside the container")
}

func TestIntegration_ProcessRunsAsCorrectUID(t *testing.T) {
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

	// Image config has no User field -> runs as root (UID 0)
	volPath := filepath.Join(w.dataDir, "volumes", "test-vol")
	content, err := os.ReadFile(filepath.Join(volPath, "uid.txt"))
	require.NoError(t, err)
	assert.Equal(t, "0\n", string(content), "default should run as root when no User in image config")
}

func TestIntegration_StopDeliversSignal(t *testing.T) {
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

	// Exec send-command inside the running container
	exitCode, stdout, stderr, err := w.Exec(ctx, id, []string{"/scripts/send-command", "test-input"})
	require.NoError(t, err, "Exec should not return an error; stderr: %s", stderr)
	assert.Equal(t, 0, exitCode, "send-command should exit 0; stderr: %s", stderr)
	assert.Contains(t, stdout, "command: test-input")

	// Verify env vars are inherited (crun exec --env passes them)
	exitCode, stdout, _, err = w.Exec(ctx, id, []string{"/bin/sh", "-c", "echo $PATH"})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode)
	assert.NotEmpty(t, strings.TrimSpace(stdout), "PATH should be inherited from container env")

	require.NoError(t, w.StopInstance(ctx, id, 5))
}

func TestIntegration_InstanceSurvivesWorkerRestart(t *testing.T) {
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

	// Discard worker 1 (simulates gamejanitor exit — container process survives independently)
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

	rt, err := runtime.New(dataDir, log)
	require.NoError(t, err, "crun runtime must be available for integration tests")

	w := &LocalWorker{
		log:       log,
		dataDir:   dataDir,
		rt:        rt,
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
