package sandbox

import (
	"fmt"
	"log/slog"
	"os/exec"
)

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
	sdArgs := []string{
		"--user", "--scope",
		"--unit=" + unitName,
	}

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
	sdArgs := []string{
		"--user", "--scope",
		"--unit=" + unitName,
	}

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

// buildExecCommand builds a bwrap command for exec (no systemd wrapping needed).
func buildExecCommand(bwrapArgs []string, bwrapPath string) *exec.Cmd {
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}
	return exec.Command(bwrapPath, bwrapArgs...)
}

// stopSystemdUnit stops a systemd transient unit gracefully.
func stopSystemdUnit(unitName string, log *slog.Logger) {
	cmd := exec.Command("systemctl", "--user", "stop", unitName+".scope")
	if err := cmd.Run(); err != nil {
		log.Debug("systemctl stop failed (may already be stopped)", "unit", unitName, "error", err)
	}
}

// killSystemdUnit force-kills a systemd transient unit.
func killSystemdUnit(unitName string, log *slog.Logger) {
	cmd := exec.Command("systemctl", "--user", "kill", "--signal=KILL", unitName+".scope")
	if err := cmd.Run(); err != nil {
		log.Debug("systemctl kill failed", "unit", unitName, "error", err)
	}
}
