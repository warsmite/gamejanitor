//go:build integration

package sandbox

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/worker"
)

// These tests require bwrap, slirp4netns, and systemd on the host.
// Run with: go test -tags integration ./worker/sandbox/

func skipIfNoBwrap(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		if _, err := os.Stat(filepath.Join(os.TempDir(), "gamejanitor-sandbox-test", "bin", "bwrap")); err != nil {
			t.Skip("bwrap not available")
		}
	}
}

func TestIntegration_BwrapRuns(t *testing.T) {
	skipIfNoBwrap(t)

	bwrap := lookupBinary("bwrap")
	require.NotEmpty(t, bwrap)

	// Run a simple command in a bwrap sandbox
	cmd := exec.Command(bwrap, "--bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-pid", "--", "/bin/echo", "hello from sandbox")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "bwrap should run: %s", string(out))
	assert.Contains(t, string(out), "hello from sandbox")
}

func TestIntegration_BwrapIsolation(t *testing.T) {
	skipIfNoBwrap(t)

	bwrap := lookupBinary("bwrap")
	tmpDir := t.TempDir()

	// Create a file outside the sandbox
	secret := filepath.Join(tmpDir, "secret.txt")
	os.WriteFile(secret, []byte("secret data"), 0644)

	// Run bwrap without binding tmpDir — the file should not be accessible
	cmd := exec.Command(bwrap,
		"--bind", "/", "/",
		"--tmpfs", tmpDir, // overlay tmpDir with empty tmpfs
		"--dev", "/dev", "--proc", "/proc", "--unshare-pid",
		"--", "/bin/cat", secret)
	out, err := cmd.CombinedOutput()

	// Should fail because the file is hidden by tmpfs
	assert.Error(t, err, "should not access file hidden by tmpfs: %s", string(out))
}

func TestIntegration_CreateAndStartInstance(t *testing.T) {
	skipIfNoBwrap(t)

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

	ctx := context.Background()

	// Create volume
	require.NoError(t, w.CreateVolume(ctx, "test-vol"))

	// Write a simple script that acts as our "game"
	scriptDir := filepath.Join(dataDir, "test-scripts")
	os.MkdirAll(scriptDir, 0755)
	os.WriteFile(filepath.Join(scriptDir, "run.sh"), []byte("#!/bin/sh\necho 'game started'\nsleep 2\necho 'game done'\n"), 0755)

	// We need a rootfs — use the host / as a simple test rootfs
	imagesDir := filepath.Join(dataDir, "images", "sha256", "test")
	os.MkdirAll(imagesDir, 0755)
	os.WriteFile(filepath.Join(imagesDir, ".extracted"), []byte("test"), 0644)

	// Create and start
	id, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       "test-instance",
		Image:      "test:latest",
		VolumeName: "test-vol",
		Env:        []string{"TEST=1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "test-instance", id)

	// Verify instance dir created
	assert.DirExists(t, filepath.Join(dataDir, "instances", id))
}

func TestIntegration_NetworkNamespace(t *testing.T) {
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

	// Cleanup
	stopSlirp(si, log)

	// Verify holder is dead
	time.Sleep(100 * time.Millisecond)
	assert.Error(t, si.holder.Process.Signal(os.Signal(nil)), "holder should be dead")
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
