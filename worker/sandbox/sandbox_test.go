package sandbox

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
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
