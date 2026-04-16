package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// BundleConfig describes an OCI container to run.
type BundleConfig struct {
	RootFS    string            // path to rootfs directory (overlay mountpoint)
	Env       []string          // "KEY=VALUE" format
	Cmd       []string          // entrypoint + args
	WorkDir   string            // working directory inside the container
	Hostname  string            // container hostname
	Binds     []Mount           // bind mounts from host into container
	UID       int               // user ID inside the container
	GID       int               // group ID inside the container
	MemoryMB  int               // memory limit in MB (0 = unlimited)
	CPUQuota  float64           // CPU limit as fraction of cores (0 = unlimited)
}

// Mount describes a bind mount into the container.
type Mount struct {
	Source      string
	Destination string
	Options     []string // e.g. ["rbind", "rw"]
}

// PrepareBundle writes the OCI config.json into bundleDir and symlinks rootfs.
// Returns the bundle directory path. bundleDir must already exist.
func PrepareBundle(bundleDir string, cfg BundleConfig) error {
	spec := buildSpec(cfg)

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling OCI config: %w", err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "config.json"), data, 0644); err != nil {
		return fmt.Errorf("writing config.json: %w", err)
	}

	rootfsLink := filepath.Join(bundleDir, "rootfs")
	os.Remove(rootfsLink)
	if err := os.Symlink(cfg.RootFS, rootfsLink); err != nil {
		return fmt.Errorf("symlinking rootfs: %w", err)
	}

	return nil
}

func buildSpec(cfg BundleConfig) map[string]any {
	mounts := defaultMounts()
	for _, b := range cfg.Binds {
		opts := b.Options
		if len(opts) == 0 {
			opts = []string{"rbind", "rw"}
		}
		mounts = append(mounts, map[string]any{
			"destination": b.Destination,
			"type":        "bind",
			"source":      b.Source,
			"options":     opts,
		})
	}

	workDir := cfg.WorkDir
	if workDir == "" {
		workDir = "/"
	}

	hostname := cfg.Hostname
	if hostname == "" {
		hostname = "gamejanitor"
	}

	process := map[string]any{
		"terminal": false,
		"user": map[string]any{
			"uid": cfg.UID,
			"gid": cfg.GID,
		},
		"args": cfg.Cmd,
		"env":  cfg.Env,
		"cwd":  workDir,
		"capabilities": defaultCapabilities(),
		"noNewPrivileges": true,
	}

	linux := map[string]any{
		"namespaces": []map[string]any{
			{"type": "pid"},
			{"type": "ipc"},
			{"type": "uts"},
			{"type": "mount"},
		},
		"maskedPaths":   defaultMaskedPaths(),
		"readonlyPaths": defaultReadonlyPaths(),
	}

	resources := map[string]any{}
	if cfg.MemoryMB > 0 {
		resources["memory"] = map[string]any{
			"limit": int64(cfg.MemoryMB) * 1024 * 1024,
		}
	}
	if cfg.CPUQuota > 0 {
		period := 100000
		quota := int(cfg.CPUQuota * float64(period))
		resources["cpu"] = map[string]any{
			"quota":  quota,
			"period": period,
		}
	}
	if len(resources) > 0 {
		linux["resources"] = resources
	}

	return map[string]any{
		"ociVersion": "1.0.0",
		"process":    process,
		"root": map[string]any{
			"path":     "rootfs",
			"readonly": false,
		},
		"hostname": hostname,
		"mounts":   mounts,
		"linux":    linux,
	}
}

func defaultMounts() []map[string]any {
	return []map[string]any{
		{
			"destination": "/proc",
			"type":        "proc",
			"source":      "proc",
		},
		{
			"destination": "/dev",
			"type":        "tmpfs",
			"source":      "tmpfs",
			"options":     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
		},
		{
			"destination": "/dev/pts",
			"type":        "devpts",
			"source":      "devpts",
			"options":     []string{"nosuid", "noexec", "newinstance", "ptmxmode=0666", "mode=0620"},
		},
		{
			"destination": "/dev/shm",
			"type":        "tmpfs",
			"source":      "shm",
			"options":     []string{"nosuid", "noexec", "nodev", "mode=1777", "size=65536k"},
		},
		{
			"destination": "/dev/mqueue",
			"type":        "mqueue",
			"source":      "mqueue",
			"options":     []string{"nosuid", "noexec", "nodev"},
		},
		{
			"destination": "/sys",
			"type":        "sysfs",
			"source":      "sysfs",
			"options":     []string{"nosuid", "noexec", "nodev", "ro"},
		},
		{
			"destination": "/tmp",
			"type":        "tmpfs",
			"source":      "tmpfs",
			"options":     []string{"nosuid", "nodev", "mode=1777"},
		},
	}
}

func defaultCapabilities() map[string]any {
	caps := []string{
		"CAP_CHOWN",
		"CAP_DAC_OVERRIDE",
		"CAP_FSETID",
		"CAP_FOWNER",
		"CAP_MKNOD",
		"CAP_NET_RAW",
		"CAP_SETGID",
		"CAP_SETUID",
		"CAP_SETFCAP",
		"CAP_SETPCAP",
		"CAP_NET_BIND_SERVICE",
		"CAP_SYS_CHROOT",
		"CAP_KILL",
		"CAP_AUDIT_WRITE",
	}
	return map[string]any{
		"bounding":    caps,
		"effective":   caps,
		"permitted":   caps,
		"ambient":     caps,
	}
}

func defaultMaskedPaths() []string {
	return []string{
		"/proc/acpi",
		"/proc/asound",
		"/proc/kcore",
		"/proc/keys",
		"/proc/latency_stats",
		"/proc/timer_list",
		"/proc/timer_stats",
		"/proc/sched_debug",
		"/sys/firmware",
		"/proc/scsi",
	}
}

func defaultReadonlyPaths() []string {
	return []string{
		"/proc/bus",
		"/proc/fs",
		"/proc/irq",
		"/proc/sys",
		"/proc/sysrq-trigger",
	}
}
