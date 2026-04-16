package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/worker"
)

func TestIsInsideDir(t *testing.T) {
	assert.True(t, isInsideDir("/tmp/extract/etc/passwd", "/tmp/extract"))
	assert.True(t, isInsideDir("/tmp/extract/a/b/c", "/tmp/extract"))
	assert.False(t, isInsideDir("/tmp/evil", "/tmp/extract"))
	assert.False(t, isInsideDir("/tmp/extract/../evil", "/tmp/extract"))
	assert.False(t, isInsideDir("/etc/passwd", "/tmp/extract"))
	assert.False(t, isInsideDir("/tmp/extract-evil/file", "/tmp/extract"))
}

func TestParseImageUser_Numeric(t *testing.T) {
	uid, gid := parseImageUser("1001", "/nonexistent")
	assert.Equal(t, 1001, uid)
	assert.Equal(t, 1001, gid)
}

func TestParseImageUser_NumericWithGroup(t *testing.T) {
	uid, gid := parseImageUser("1001:1002", "/nonexistent")
	assert.Equal(t, 1001, uid)
	assert.Equal(t, 1002, gid)
}

func TestParseImageUser_Empty(t *testing.T) {
	uid, gid := parseImageUser("", "/nonexistent")
	assert.Equal(t, 0, uid)
	assert.Equal(t, 0, gid)
}

func TestParseImageUser_Username(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(dir+"/etc", 0755)
	os.WriteFile(dir+"/etc/passwd", []byte("gameserver:x:1001:1001::/home/gameserver:/bin/bash\n"), 0644)

	uid, gid := parseImageUser("gameserver", dir)
	assert.Equal(t, 1001, uid)
	assert.Equal(t, 1001, gid)
}

func TestSaveAndLoadInstanceState(t *testing.T) {
	dir := t.TempDir()
	original := instanceState{
		StartedAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
	}

	require.NoError(t, saveInstanceState(dir, original))

	loaded, err := loadInstanceState(dir)
	require.NoError(t, err)
	assert.Equal(t, original.StartedAt.UTC(), loaded.StartedAt.UTC())
}

func TestLoadInstanceState_Missing(t *testing.T) {
	_, err := loadInstanceState(t.TempDir())
	assert.Error(t, err)
}

func TestLoadInstanceState_Corrupt(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/state.json", []byte("{invalid"), 0644)
	_, err := loadInstanceState(dir)
	assert.Error(t, err)
}

func TestRotatingWriter_RotatesAtLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")

	w, err := newRotatingWriter(path)
	require.NoError(t, err)

	// Write exactly logMaxBytes + 1 byte to trigger rotation
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	written := 0
	for written < logMaxBytes {
		n, err := w.Write(chunk)
		require.NoError(t, err)
		written += n
	}

	// This write should trigger rotation
	_, err = w.Write([]byte("after-rotation\n"))
	require.NoError(t, err)
	w.Close()

	// output.log should contain only the post-rotation data
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "after-rotation\n", string(content))

	// output.log.0 should exist (the pre-rotation data)
	_, err = os.Stat(path + ".0")
	assert.NoError(t, err, "rotated backup should exist")
}

func TestRotatingWriter_NoRotationUnderLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.log")

	w, err := newRotatingWriter(path)
	require.NoError(t, err)

	w.Write([]byte("small log\n"))
	w.Close()

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "small log\n", string(content))

	_, err = os.Stat(path + ".0")
	assert.True(t, os.IsNotExist(err), "no backup should exist for small logs")
}

func TestCreateInstance_RejectsEmptyFields(t *testing.T) {
	w := &LocalWorker{
		instances: make(map[string]*managedInstance),
		dataDir:   t.TempDir(),
	}
	ctx := context.Background()

	_, err := w.CreateInstance(ctx, worker.InstanceOptions{})
	assert.ErrorContains(t, err, "instance name is required")

	_, err = w.CreateInstance(ctx, worker.InstanceOptions{Name: "test"})
	assert.ErrorContains(t, err, "instance image is required")
}
