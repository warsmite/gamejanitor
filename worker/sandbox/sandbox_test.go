package sandbox

import (
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

	// Should bind rootfs to /
	idx := indexOf(args, "--bind")
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

func indexOf(s []string, target string) int {
	for i, v := range s {
		if v == target {
			return i
		}
	}
	return -1
}
