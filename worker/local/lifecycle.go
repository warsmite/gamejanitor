package local

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// buildSystemdCrunCommand wraps crun run in systemd-run for lifecycle management.
// Resource limits (memory, CPU) are handled by crun via the OCI config.json,
// so the systemd scope is only used for lifecycle (stop/kill/recover).
func buildSystemdCrunCommand(id string, bundleDir string, rt *runtime.Runtime, paths *systemPaths) *exec.Cmd {
	crunArgs := []string{"--root", rt.StateDir(), "run", "--bundle", bundleDir, id}
	innerArgs := append([]string{rt.CrunPath()}, crunArgs...)

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

	sdArgs = append(sdArgs, "--")
	sdArgs = append(sdArgs, innerArgs...)

	systemdRunPath := lookupBinary("systemd-run")
	if systemdRunPath == "" {
		systemdRunPath = "systemd-run"
	}
	return exec.Command(systemdRunPath, sdArgs...)
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

// isSystemdScopeActive checks whether a systemd scope unit is currently active.
func isSystemdScopeActive(unitName string, paths *systemPaths) bool {
	if !paths.hasSystemd() {
		return false
	}
	scope := unitName + ".scope"
	prefix := systemctlPrefix(paths)
	args := append(prefix, "is-active", "--quiet", scope)
	return exec.Command(paths.Systemctl, args...).Run() == nil
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
	data, err := os.ReadFile(procsPath)
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
