package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// PastaNet manages a container's network via pasta. Pasta creates the
// user+network namespace and runs a worker process inside it. The worker
// runs crun create/start and pumps container stdio. This avoids all
// setns/namespace-joining — crun inherits the namespace from the parent.
// Works identically for root and rootless.
type PastaNet struct {
	pastaCmd *exec.Cmd
	done     chan struct{}
	exitCode int
}

// StartContainerWithPasta launches a container inside a pasta-managed network
// namespace. The flow:
//  1. pasta creates user+net namespace and runs our binary as a worker inside it
//  2. The worker runs crun create → crun start inside the namespace
//  3. Container stdio is pumped back to the provided writers
//  4. When the container exits, the worker reports the exit code and exits
//
// Returns a ContainerHandle for waiting on exit.
func (r *Runtime) StartContainerWithPasta(
	id, bundleDir string,
	forwards []PortForward,
	stdout, stderr io.Writer,
) (*ContainerHandle, error) {

	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding self executable: %w", err)
	}

	// Build pasta args: port forwards + our worker as the command
	pastaArgs := []string{
		"--ns-ifname", "eth0",
		"--config-net",
		"--quiet",
	}
	for _, fwd := range forwards {
		flag := "-t"
		if fwd.Protocol == "udp" {
			flag = "-u"
		}
		if fwd.HostPort == fwd.ContainerPort {
			pastaArgs = append(pastaArgs, flag, fmt.Sprintf("%d", fwd.HostPort))
		} else {
			pastaArgs = append(pastaArgs, flag, fmt.Sprintf("%d:%d", fwd.HostPort, fwd.ContainerPort))
		}
	}

	// The worker command: our binary re-exec'd with env vars telling it
	// to run crun create+start inside the namespace
	workerEnv := fmt.Sprintf("%s|%s|%s|%s", r.crunPath, r.stateDir, id, bundleDir)
	pastaArgs = append(pastaArgs, "--", self)

	cmd := exec.Command(r.pastaPath, pastaArgs...)
	cmd.Env = append(os.Environ(), "_GAMEJANITOR_CRUN_WORKER="+workerEnv)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting pasta: %w", err)
	}

	// Wait for the worker to signal that crun start succeeded.
	// The worker writes "STARTED <pid>\n" to a status pipe.
	// For now, use a simple time-based wait since the worker will start
	// writing container stdout once crun starts.
	// TODO: use a proper status pipe

	h := &ContainerHandle{
		cmd:      cmd,
		done:     make(chan struct{}),
		exitCode: -1,
	}

	go func() {
		cmd.Wait()
		h.exitCode = cmd.ProcessState.ExitCode()
		close(h.done)
	}()

	// Wait briefly for the container to actually start (pasta + crun create + start)
	time.Sleep(500 * time.Millisecond)

	// Try to get the container PID from crun state
	cs, err := r.State(id)
	if err == nil {
		h.PID = cs.PID
	}

	r.log.Info("container started via pasta",
		"container", id,
		"pasta_pid", cmd.Process.Pid,
		"container_pid", h.PID,
		"forwards", len(forwards))

	return h, nil
}

// MaybeHandleCrunWorker handles the re-exec'd worker case. Called early in
// main(). If _GAMEJANITOR_CRUN_WORKER is set, this process was launched by
// pasta inside a network namespace. It runs crun create + start, pumps stdio,
// and exits with the container's exit code.
func MaybeHandleCrunWorker() bool {
	spec := os.Getenv("_GAMEJANITOR_CRUN_WORKER")
	if spec == "" {
		return false
	}

	parts := strings.SplitN(spec, "|", 4)
	if len(parts) != 4 {
		fmt.Fprintf(os.Stderr, "invalid worker spec: %s\n", spec)
		os.Exit(1)
	}
	crunPath, stateDir, id, bundleDir := parts[0], parts[1], parts[2], parts[3]

	exitCode := runCrunWorker(crunPath, stateDir, id, bundleDir)
	os.Exit(exitCode)
	return true
}

func runCrunWorker(crunPath, stateDir, id, bundleDir string) int {
	crunCmd := func(args ...string) *exec.Cmd {
		fullArgs := append([]string{"--root", stateDir}, args...)
		return exec.Command(crunPath, fullArgs...)
	}

	// Create container (synchronous, process paused)
	createCmd := crunCmd("create", "--bundle", bundleDir, id)
	createCmd.Stdout = os.Stdout
	createCmd.Stderr = os.Stderr

	if err := createCmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "crun create: %v\n", err)
		return 1
	}

	// Start container (unpause)
	startCmd := crunCmd("start", id)
	if out, err := startCmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "crun start: %v\n%s", err, out)
		crunCmd("delete", "--force", id).Run()
		return 1
	}

	// Forward signals to the container
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for sig := range sigCh {
			crunCmd("kill", id, fmt.Sprintf("%d", sig.(syscall.Signal))).Run()
		}
	}()

	// Poll crun state for exit
	for {
		time.Sleep(500 * time.Millisecond)
		out, err := crunCmd("state", id).Output()
		if err != nil {
			break
		}
		var state struct {
			Status string `json:"status"`
		}
		json.Unmarshal(out, &state)
		if state.Status == "stopped" {
			break
		}
	}

	signal.Stop(sigCh)

	// Get exit code via Wait4 on the container PID
	exitCode := 0
	out, _ := crunCmd("state", id).Output()
	if out != nil {
		var state struct {
			PID int `json:"pid"`
		}
		json.Unmarshal(out, &state)
		if state.PID > 0 {
			var ws syscall.WaitStatus
			wpid, _ := syscall.Wait4(state.PID, &ws, syscall.WNOHANG, nil)
			if wpid == state.PID && ws.Exited() {
				exitCode = ws.ExitStatus()
			} else if wpid == state.PID && ws.Signaled() {
				exitCode = 128 + int(ws.Signal())
			}
		}
	}

	crunCmd("delete", "--force", id).Run()
	return exitCode
}

// PortForward describes a host-to-container port mapping via pasta.
type PortForward struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}
