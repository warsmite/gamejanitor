package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// systemdRunArgs returns the base args for systemd-run.
// Uses --user scope when running as a regular user, system scope when root.
func systemdRunArgs(unitName string) []string {
	if os.Getuid() == 0 {
		return []string{"--scope", "--unit=" + unitName}
	}
	return []string{"--user", "--scope", "--unit=" + unitName}
}

// hasSystemdRun checks if systemd-run is available on the host.
func hasSystemdRun() bool {
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// buildSystemdCommand wraps bwrap in a systemd transient unit with resource limits
// and port restrictions. Falls back to raw bwrap if systemd is unavailable.
func buildSystemdCommand(id string, manifest instanceManifest, bwrapArgs []string, bwrapPath string) *exec.Cmd {
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}

	if !hasSystemdRun() {
		args := append([]string{"--die-with-parent"}, bwrapArgs...)
		return exec.Command(bwrapPath, args...)
	}

	unitName := "gj-" + id
	sdArgs := systemdRunArgs(unitName)

	// Resource limits via cgroups v2
	if manifest.MemoryLimitMB > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=MemoryMax=%dM", manifest.MemoryLimitMB))
	}
	if manifest.CPULimit > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=CPUQuota=%d%%", int(manifest.CPULimit*100)))
	}

	// Port restriction via cgroups v2 BPF — only allow binding allocated ports
	for _, p := range manifest.Ports {
		if p.HostPort > 0 {
			sdArgs = append(sdArgs, fmt.Sprintf("--property=SocketBindAllow=%d", p.HostPort))
		}
	}
	if len(manifest.Ports) > 0 {
		sdArgs = append(sdArgs, "--property=SocketBindDeny=any")
	}

	sdArgs = append(sdArgs, "--", bwrapPath)
	sdArgs = append(sdArgs, bwrapArgs...)

	return exec.Command("systemd-run", sdArgs...)
}

// buildSystemdCommandWithNetns wraps bwrap in systemd-run, optionally using
// nsenter to join an existing network namespace from slirp4netns.
func buildSystemdCommandWithNetns(id string, manifest instanceManifest, bwrapArgs []string, bwrapPath string, si *slirpInstance) *exec.Cmd {
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}

	// Build the actual command: nsenter (if netns) + bwrap
	var innerArgs []string
	if si != nil {
		innerArgs = append(nsenterPrefix(si.nsPID), bwrapPath)
	} else {
		innerArgs = []string{bwrapPath}
	}
	innerArgs = append(innerArgs, bwrapArgs...)

	if !hasSystemdRun() {
		return exec.Command(innerArgs[0], innerArgs[1:]...)
	}

	unitName := "gj-" + id
	sdArgs := systemdRunArgs(unitName)

	if manifest.MemoryLimitMB > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=MemoryMax=%dM", manifest.MemoryLimitMB))
	}
	if manifest.CPULimit > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=CPUQuota=%d%%", int(manifest.CPULimit*100)))
	}

	for _, p := range manifest.Ports {
		if p.HostPort > 0 {
			sdArgs = append(sdArgs, fmt.Sprintf("--property=SocketBindAllow=%d", p.HostPort))
		}
	}
	if len(manifest.Ports) > 0 {
		sdArgs = append(sdArgs, "--property=SocketBindDeny=any")
	}

	sdArgs = append(sdArgs, "--")
	sdArgs = append(sdArgs, innerArgs...)

	return exec.Command("systemd-run", sdArgs...)
}

// killCgroupProcesses sends a signal to all processes in a systemd scope's cgroup.
// This reaches through namespace boundaries since cgroup PIDs are host-visible.
func killCgroupProcesses(unitName string, sig syscall.Signal, log *slog.Logger) {
	// Find the cgroup path for this scope
	pattern := fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/%s.scope/cgroup.procs",
		os.Getuid(), os.Getuid(), unitName)

	data, err := os.ReadFile(pattern)
	if err != nil {
		// Try to find it via systemctl
		out, err := exec.Command("systemctl", "--user", "show", "-p", "ControlGroup", unitName+".scope").Output()
		if err != nil {
			log.Debug("could not find cgroup for unit", "unit", unitName, "error", err)
			return
		}
		cgPath := strings.TrimPrefix(strings.TrimSpace(string(out)), "ControlGroup=")
		if cgPath == "" {
			return
		}
		procsPath := filepath.Join("/sys/fs/cgroup", cgPath, "cgroup.procs")
		data, err = os.ReadFile(procsPath)
		if err != nil {
			return
		}
	}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		syscall.Kill(pid, sig)
	}
}

// buildExecCommand builds a bwrap command for exec (no systemd wrapping needed).
func buildExecCommand(bwrapArgs []string, bwrapPath string) *exec.Cmd {
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}
	return exec.Command(bwrapPath, bwrapArgs...)
}

func systemctlArgs(args ...string) []string {
	if os.Getuid() != 0 {
		return append([]string{"--user"}, args...)
	}
	return args
}

// stopSystemdUnit stops a systemd transient unit gracefully.
func stopSystemdUnit(unitName string, log *slog.Logger) {
	cmd := exec.Command(findBinary("systemctl"), systemctlArgs("stop", unitName+".scope")...)
	if err := cmd.Run(); err != nil {
		log.Debug("systemctl stop failed (may already be stopped)", "unit", unitName, "error", err)
	}
}

// killSystemdUnit force-kills a systemd transient unit.
func killSystemdUnit(unitName string, log *slog.Logger) {
	cmd := exec.Command(findBinary("systemctl"), systemctlArgs("kill", "--signal=KILL", unitName+".scope")...)
	if err := cmd.Run(); err != nil {
		log.Debug("systemctl kill failed", "unit", unitName, "error", err)
	}
}
