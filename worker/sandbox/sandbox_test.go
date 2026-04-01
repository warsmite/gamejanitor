package sandbox

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/warsmite/gamejanitor/worker"
)

func TestBuildBwrapArgs_BasicStructure(t *testing.T) {
	manifest := instanceManifest{
		VolumeName: "test-vol",
		Env:        []string{"GAME_PORT=27015"},
	}
	imgCfg := &imageConfig{
		Env:        []string{"PATH=/usr/bin"},
		WorkingDir: "/data",
	}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gamejanitor")

	// Should have basic sandbox flags
	assert.Contains(t, args, "--unshare-pid")
	assert.Contains(t, args, "--dev")
	assert.Contains(t, args, "--proc")
	assert.Contains(t, args, "--tmpfs")

	// Should read-only bind rootfs to /
	idx := indexOf(args, "--ro-bind")
	assert.GreaterOrEqual(t, idx, 0)
	assert.Equal(t, "/tmp/rootfs", args[idx+1])
	assert.Equal(t, "/", args[idx+2])

	// Should set working directory
	assert.Contains(t, args, "--chdir")

	// Should pass env vars
	envIdx := indexOf(args, "--setenv")
	assert.GreaterOrEqual(t, envIdx, 0)
}

func TestBuildBwrapArgs_VolumeBind(t *testing.T) {
	manifest := instanceManifest{VolumeName: "my-volume"}
	imgCfg := &imageConfig{}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gj")

	// Should bind volume to /data
	joined := strings.Join(args, " ")
	assert.Contains(t, joined, "/tmp/gj/volumes/my-volume")
	assert.Contains(t, joined, "/data")
}

func TestBuildBwrapArgs_BindMounts(t *testing.T) {
	manifest := instanceManifest{
		Binds: []string{
			"/host/scripts:/scripts:ro",
			"/host/data:/mnt/data",
		},
	}
	imgCfg := &imageConfig{}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gj")
	joined := strings.Join(args, " ")

	assert.Contains(t, joined, "--ro-bind /host/scripts /scripts")
	assert.Contains(t, joined, "--bind /host/data /mnt/data")
}

func TestBuildBwrapArgs_NoVolumeNoError(t *testing.T) {
	manifest := instanceManifest{}
	imgCfg := &imageConfig{}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gj")
	assert.NotEmpty(t, args)
}

func TestSystemPaths_HasNetworkIsolation(t *testing.T) {
	p := &systemPaths{
		Slirp4netns: "/usr/bin/slirp4netns",
		Unshare:     "/usr/bin/unshare",
		Sh:          "/bin/sh",
		Sleep:       "/bin/sleep",
	}
	assert.True(t, p.hasNetworkIsolation())

	p.Slirp4netns = ""
	assert.False(t, p.hasNetworkIsolation())
}

func TestSystemPaths_HasSystemd(t *testing.T) {
	p := &systemPaths{Systemctl: "/usr/bin/systemctl"}
	assert.True(t, p.hasSystemd())

	p.Systemctl = ""
	assert.False(t, p.hasSystemd())
}

func TestSystemPaths_HasUIDMapping(t *testing.T) {
	p := &systemPaths{NewUIDMap: "/usr/bin/newuidmap", NewGIDMap: "/usr/bin/newgidmap"}
	assert.True(t, p.hasUIDMapping())

	p.NewUIDMap = ""
	assert.False(t, p.hasUIDMapping())
}

func TestNsenterPrefix_Root(t *testing.T) {
	paths := &systemPaths{Nsenter: "/usr/bin/nsenter", IsRoot: true}
	prefix := nsenterPrefix(12345, paths)

	assert.Contains(t, prefix, "/usr/bin/nsenter")
	assert.Contains(t, prefix, "--net=/proc/12345/ns/net")
	// Root should NOT enter user namespace
	for _, arg := range prefix {
		assert.NotContains(t, arg, "--user=")
	}
}

func TestNsenterPrefix_NonRoot(t *testing.T) {
	paths := &systemPaths{Nsenter: "/usr/bin/nsenter", IsRoot: false}
	prefix := nsenterPrefix(12345, paths)

	assert.Contains(t, prefix, "--preserve-credentials")
	assert.Contains(t, prefix, "--user=/proc/12345/ns/user")
	assert.Contains(t, prefix, "--net=/proc/12345/ns/net")
}

func TestLookupBinary_FindsInPath(t *testing.T) {
	// sh should always be findable
	path := lookupBinary("sh")
	assert.NotEmpty(t, path)
}

func TestLookupBinary_ReturnsEmptyForMissing(t *testing.T) {
	path := lookupBinary("nonexistent-binary-12345")
	assert.Empty(t, path)
}

func TestSubUIDRange_ReadsFromSystem(t *testing.T) {
	start, count := SubUIDRange()
	// Should return valid range (either from /etc/subuid or fallback)
	assert.Greater(t, start, 0)
	assert.Greater(t, count, 0)
}

func TestSubUIDRange_Fallback(t *testing.T) {
	// Non-existent file should return defaults
	start, count := readSubRange("/nonexistent/path")
	assert.Equal(t, 165536, start)
	assert.Equal(t, 65536, count)
}

func TestFindSandboxInitPID_NoMatchForInvalidParent(t *testing.T) {
	paths := &systemPaths{Systemctl: ""}
	// PID 99999999 doesn't exist, no children should be found
	pid := findSandboxInitPID("nonexistent", 99999999, paths)
	assert.Equal(t, 0, pid)
}

func TestInstanceManifest_PortsRoundtrip(t *testing.T) {
	manifest := instanceManifest{
		Ports: []worker.PortBinding{
			{HostPort: 27000, InstancePort: 27000, Protocol: "udp"},
			{HostPort: 27001, InstancePort: 27001, Protocol: "tcp"},
		},
	}
	assert.Len(t, manifest.Ports, 2)
	assert.Equal(t, 27000, manifest.Ports[0].HostPort)
}

func TestBuildBwrapArgs_NamespaceIsolation(t *testing.T) {
	manifest := instanceManifest{}
	imgCfg := &imageConfig{}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gj")

	// Should isolate all namespaces
	assert.Contains(t, args, "--unshare-pid")
	assert.Contains(t, args, "--unshare-ipc")
	assert.Contains(t, args, "--unshare-uts")
	assert.Contains(t, args, "--unshare-cgroup")
	assert.Contains(t, args, "--die-with-parent")
	assert.Contains(t, args, "--new-session")
}

func TestIsInsideDir(t *testing.T) {
	assert.True(t, isInsideDir("/tmp/extract/etc/passwd", "/tmp/extract"))
	assert.True(t, isInsideDir("/tmp/extract/a/b/c", "/tmp/extract"))
	assert.False(t, isInsideDir("/tmp/evil", "/tmp/extract"))
	assert.False(t, isInsideDir("/tmp/extract/../evil", "/tmp/extract"))
	assert.False(t, isInsideDir("/etc/passwd", "/tmp/extract"))
	// Edge case: prefix match but not a subdirectory
	assert.False(t, isInsideDir("/tmp/extract-evil/file", "/tmp/extract"))
}

func TestCreateInstance_ValidatesRequiredFields(t *testing.T) {
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
	// Create a fake passwd file
	dir := t.TempDir()
	os.MkdirAll(dir+"/etc", 0755)
	os.WriteFile(dir+"/etc/passwd", []byte("gameserver:x:1001:1001::/home/gameserver:/bin/bash\n"), 0644)

	uid, gid := parseImageUser("gameserver", dir)
	assert.Equal(t, 1001, uid)
	assert.Equal(t, 1001, gid)
}

func TestBuildBwrapArgs_SetsUIDGID(t *testing.T) {
	manifest := instanceManifest{}
	imgCfg := &imageConfig{User: "1001:1001"}

	args := buildBwrapArgs("/tmp/rootfs", manifest, imgCfg, "/tmp/gj")
	assert.Contains(t, args, "--uid")
	assert.Contains(t, args, "1001")
	assert.Contains(t, args, "--gid")
}

func indexOf(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}
