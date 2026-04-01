package sandbox

import (
	"context"
	"os"
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
		StartedAt:   time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC),
		HolderPID:   1234,
		SlirpPID:    5678,
		NsPID:       1234,
		SlirpSocket: "/tmp/slirp.sock",
		UnitName:    "gj-test-instance",
	}

	require.NoError(t, saveInstanceState(dir, original))

	loaded, err := loadInstanceState(dir)
	require.NoError(t, err)
	assert.Equal(t, original.StartedAt.UTC(), loaded.StartedAt.UTC())
	assert.Equal(t, original.HolderPID, loaded.HolderPID)
	assert.Equal(t, original.SlirpPID, loaded.SlirpPID)
	assert.Equal(t, original.NsPID, loaded.NsPID)
	assert.Equal(t, original.SlirpSocket, loaded.SlirpSocket)
	assert.Equal(t, original.UnitName, loaded.UnitName)
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

func TestIsPIDAlive_CurrentProcess(t *testing.T) {
	// Our own PID should be alive and contain "go" in cmdline (test binary)
	assert.True(t, isPIDAlive(os.Getpid(), "sandbox.test", "go"))
}

func TestIsPIDAlive_NonexistentPID(t *testing.T) {
	assert.False(t, isPIDAlive(999999999, "anything"))
}

func TestIsPIDAlive_WrongName(t *testing.T) {
	// Our PID is alive but shouldn't match "slirp4netns"
	assert.False(t, isPIDAlive(os.Getpid(), "slirp4netns"))
}

func TestCreateInstance_RejectsEmptyFields(t *testing.T) {
	w := &SandboxWorker{
		instances: make(map[string]*managedInstance),
		dataDir:   t.TempDir(),
	}
	ctx := context.Background()

	_, err := w.CreateInstance(ctx, worker.InstanceOptions{})
	assert.ErrorContains(t, err, "instance name is required")

	_, err = w.CreateInstance(ctx, worker.InstanceOptions{Name: "test"})
	assert.ErrorContains(t, err, "instance image is required")
}
