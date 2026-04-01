package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// buildSystemdCommandWithNetns wraps bwrap in systemd-run, optionally using
// nsenter to join an existing network namespace from slirp4netns.
func buildSystemdCommandWithNetns(id string, manifest instanceManifest, bwrapArgs []string, paths *systemPaths, si *slirpInstance) *exec.Cmd {
	// Build the actual command: nsenter (if netns) + bwrap
	var innerArgs []string
	if si != nil && paths.Nsenter != "" {
		innerArgs = append(nsenterPrefix(si.nsPID, paths), paths.Bwrap)
	} else {
		innerArgs = []string{paths.Bwrap}
	}
	innerArgs = append(innerArgs, bwrapArgs...)

	if !paths.hasSystemd() {
		return exec.Command(innerArgs[0], innerArgs[1:]...)
	}

	unitName := "gj-" + id
	var sdArgs []string
	if paths.IsRoot {
		sdArgs = []string{"--scope", "--unit=" + unitName}
	} else {
		sdArgs = []string{"--user", "--scope", "--unit=" + unitName}
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

	systemdRunPath := lookupBinary("systemd-run")
	if systemdRunPath == "" {
		systemdRunPath = "systemd-run"
	}
	return exec.Command(systemdRunPath, sdArgs...)
}

// buildExecCommand builds a bwrap command for exec (no systemd wrapping needed).
func buildExecCommand(bwrapArgs []string, bwrapPath string) *exec.Cmd {
	if bwrapPath == "" {
		bwrapPath = "bwrap"
	}
	return exec.Command(bwrapPath, bwrapArgs...)
}

// stopSystemdUnit stops a systemd transient unit and cleans up its state.
func stopSystemdUnit(unitName string, paths *systemPaths, log *slog.Logger) {
	if !paths.hasSystemd() {
		return
	}
	scope := unitName + ".scope"
	prefix := systemctlPrefix(paths)

	// Stop the scope
	cmd := exec.Command(paths.Systemctl, append(prefix, "stop", scope)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Debug("systemctl stop failed (may already be stopped)", "unit", unitName, "error", err, "output", string(out))
	}

	// Reset the failed state so it doesn't accumulate
	exec.Command(paths.Systemctl, append(prefix, "reset-failed", scope)...).Run()
}

// killSystemdUnit force-kills a systemd transient unit.
func killSystemdUnit(unitName string, paths *systemPaths, log *slog.Logger) {
	if !paths.hasSystemd() {
		return
	}
	scope := unitName + ".scope"
	prefix := systemctlPrefix(paths)

	exec.Command(paths.Systemctl, append(prefix, "kill", "--signal=KILL", scope)...).Run()
	exec.Command(paths.Systemctl, append(prefix, "reset-failed", scope)...).Run()
}

// systemctlPrefix returns --user for non-root, empty for root.
func systemctlPrefix(paths *systemPaths) []string {
	if !paths.IsRoot {
		return []string{"--user"}
	}
	return nil
}

// killCgroupProcesses sends a signal to all processes in a systemd scope's cgroup.
func killCgroupProcesses(unitName string, sig syscall.Signal, paths *systemPaths, log *slog.Logger) {
	if !paths.hasSystemd() {
		return
	}

	// Read cgroup path from systemctl
	prefix := systemctlPrefix(paths)
	args := append(prefix, "show", "-p", "ControlGroup", unitName+".scope")
	out, err := exec.Command(paths.Systemctl, args...).Output()
	if err != nil {
		log.Debug("could not find cgroup for unit", "unit", unitName, "error", err)
		return
	}

	cgPath := strings.TrimPrefix(strings.TrimSpace(string(out)), "ControlGroup=")
	if cgPath == "" {
		return
	}

	procsPath := "/sys/fs/cgroup" + cgPath + "/cgroup.procs"
	data, err := readFileQuiet(procsPath)
	if err != nil {
		return
	}

	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil || pid <= 0 {
			continue
		}
		syscall.Kill(pid, sig)
	}
}

func readFileQuiet(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	return data, err
}
