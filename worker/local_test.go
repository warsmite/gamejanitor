//go:build integration

package worker_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/docker"
	"github.com/warsmite/gamejanitor/worker"
)

func newTestLocalWorker(t *testing.T) *worker.LocalWorker {
	t.Helper()
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	dockerClient, err := docker.New(log, "")
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	dataDir := t.TempDir()
	return worker.NewLocalWorker(dockerClient, nil, dataDir, log)
}

// Unique names to avoid collisions with other test runs
func testVolumeName(t *testing.T) string {
	return "gamejanitor-test-" + t.Name()
}

func testContainerName(t *testing.T) string {
	return "gamejanitor-test-" + t.Name()
}

func TestWorker_ContainerLifecycle(t *testing.T) {
	w := newTestLocalWorker(t)
	ctx := context.Background()
	containerName := testContainerName(t)

	// Pull a small image
	require.NoError(t, w.PullImage(ctx, "alpine:latest"))

	// Create container
	id, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       containerName,
		Image:      "alpine:latest",
		Entrypoint: []string{"sleep", "30"},
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	t.Cleanup(func() {
		w.StopContainer(context.Background(), id, 1)
		w.RemoveContainer(context.Background(), id)
	})

	// Start
	require.NoError(t, w.StartContainer(ctx, id))

	// Inspect — should be running
	info, err := w.InspectContainer(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "running", info.State)

	// Stats
	stats, err := w.ContainerStats(ctx, id)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stats.MemoryLimitMB, 0)

	// Stop
	require.NoError(t, w.StopContainer(ctx, id, 5))

	// Inspect — should be exited
	info, err = w.InspectContainer(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "exited", info.State)

	// Remove
	require.NoError(t, w.RemoveContainer(ctx, id))
}

func TestWorker_VolumeOperations(t *testing.T) {

	w := newTestLocalWorker(t)
	ctx := context.Background()
	volName := testVolumeName(t)

	// Create
	require.NoError(t, w.CreateVolume(ctx, volName))
	t.Cleanup(func() { w.RemoveVolume(context.Background(), volName) })

	// Write a file
	require.NoError(t, w.WriteFile(ctx, volName, "/test.txt", []byte("hello world"), 0644))

	// Read it back
	data, err := w.ReadFile(ctx, volName, "/test.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello world", string(data))

	// List files
	entries, err := w.ListFiles(ctx, volName, "/")
	require.NoError(t, err)
	found := false
	for _, e := range entries {
		if e.Name == "test.txt" {
			found = true
			assert.False(t, e.IsDir)
		}
	}
	assert.True(t, found, "test.txt should appear in listing")

	// Create directory
	require.NoError(t, w.CreateDirectory(ctx, volName, "/subdir"))

	// Write file in subdir
	require.NoError(t, w.WriteFile(ctx, volName, "/subdir/nested.txt", []byte("nested"), 0644))

	// Read nested file
	data, err = w.ReadFile(ctx, volName, "/subdir/nested.txt")
	require.NoError(t, err)
	assert.Equal(t, "nested", string(data))

	// Delete file
	require.NoError(t, w.DeletePath(ctx, volName, "/test.txt"))

	// Verify deleted
	_, err = w.ReadFile(ctx, volName, "/test.txt")
	assert.Error(t, err)

	// Volume size should be > 0
	size, err := w.VolumeSize(ctx, volName)
	require.NoError(t, err)
	assert.Greater(t, size, int64(0))
}

func TestWorker_BackupRestoreRoundTrip(t *testing.T) {

	w := newTestLocalWorker(t)
	ctx := context.Background()
	volName := testVolumeName(t)

	// Create volume and write some data
	require.NoError(t, w.CreateVolume(ctx, volName))
	t.Cleanup(func() { w.RemoveVolume(context.Background(), volName) })

	require.NoError(t, w.WriteFile(ctx, volName, "/data.txt", []byte("backup me"), 0644))
	require.NoError(t, w.CreateDirectory(ctx, volName, "/configs"))
	require.NoError(t, w.WriteFile(ctx, volName, "/configs/server.cfg", []byte("setting=value"), 0644))

	// Backup
	tarReader, err := w.BackupVolume(ctx, volName)
	require.NoError(t, err)
	backupData, err := io.ReadAll(tarReader)
	tarReader.Close()
	require.NoError(t, err)
	assert.Greater(t, len(backupData), 0, "backup should produce data")

	// Delete original data
	require.NoError(t, w.DeletePath(ctx, volName, "/data.txt"))
	require.NoError(t, w.DeletePath(ctx, volName, "/configs"))

	// Verify deleted
	_, err = w.ReadFile(ctx, volName, "/data.txt")
	assert.Error(t, err)

	// Restore from backup
	require.NoError(t, w.RestoreVolume(ctx, volName, newBytesReader(backupData)))

	// Verify restored data
	data, err := w.ReadFile(ctx, volName, "/data.txt")
	require.NoError(t, err)
	assert.Equal(t, "backup me", string(data))

	cfg, err := w.ReadFile(ctx, volName, "/configs/server.cfg")
	require.NoError(t, err)
	assert.Equal(t, "setting=value", string(cfg))
}

func TestWorker_FilePathTraversal(t *testing.T) {

	w := newTestLocalWorker(t)
	ctx := context.Background()
	volName := testVolumeName(t)

	require.NoError(t, w.CreateVolume(ctx, volName))
	t.Cleanup(func() { w.RemoveVolume(context.Background(), volName) })

	_, err := w.ReadFile(ctx, volName, "../../etc/passwd")
	assert.Error(t, err, "path traversal should be rejected")

	err = w.WriteFile(ctx, volName, "../../tmp/evil", []byte("pwned"), 0644)
	assert.Error(t, err, "path traversal should be rejected")

	_, err = w.ListFiles(ctx, volName, "../../../")
	assert.Error(t, err, "path traversal should be rejected")
}

func TestWorker_Rename(t *testing.T) {

	w := newTestLocalWorker(t)
	ctx := context.Background()
	volName := testVolumeName(t)

	require.NoError(t, w.CreateVolume(ctx, volName))
	t.Cleanup(func() { w.RemoveVolume(context.Background(), volName) })

	require.NoError(t, w.WriteFile(ctx, volName, "/old.txt", []byte("content"), 0644))
	require.NoError(t, w.RenamePath(ctx, volName, "/old.txt", "/new.txt"))

	_, err := w.ReadFile(ctx, volName, "/old.txt")
	assert.Error(t, err, "old path should not exist")

	data, err := w.ReadFile(ctx, volName, "/new.txt")
	require.NoError(t, err)
	assert.Equal(t, "content", string(data))
}

func TestWorker_WatchEvents(t *testing.T) {
	w := newTestLocalWorker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	events, errs := w.WatchEvents(ctx)

	// Pull and start a container to generate events
	require.NoError(t, w.PullImage(ctx, "alpine:latest"))
	containerName := testContainerName(t)
	id, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       containerName,
		Image:      "alpine:latest",
		Entrypoint: []string{"sleep", "5"},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		w.StopContainer(context.Background(), id, 1)
		w.RemoveContainer(context.Background(), id)
	})

	require.NoError(t, w.StartContainer(ctx, id))

	// Should receive a "start" event
	select {
	case evt := <-events:
		assert.Equal(t, "start", evt.Action)
		assert.Equal(t, id, evt.ContainerID)
	case err := <-errs:
		t.Fatalf("error watching events: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for start event")
	}
}

func newBytesReader(data []byte) io.Reader {
	return bytes.NewReader(data)
}
