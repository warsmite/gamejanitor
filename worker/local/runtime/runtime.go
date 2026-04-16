package runtime

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/worker/local/runtime/embedded"
)

const crunVersion = "1.24"

// Runtime wraps crun for OCI container lifecycle operations.
type Runtime struct {
	crunPath  string
	pastaPath string
	stateDir  string
	log       *slog.Logger
}

// New creates a Runtime, extracting embedded binaries if needed.
func New(dataDir string, log *slog.Logger) (*Runtime, error) {
	crunPath, err := ensureBinary(dataDir, "crun", crunVersion, log)
	if err != nil {
		return nil, fmt.Errorf("crun not available: %w", err)
	}

	pastaPath, err := ensureBinary(dataDir, "pasta", "", log)
	if err != nil {
		return nil, fmt.Errorf("pasta not available: %w", err)
	}

	stateDir := filepath.Join(dataDir, "crun-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("creating crun state dir: %w", err)
	}

	log.Info("runtime ready", "crun", crunPath, "pasta", pastaPath, "state_dir", stateDir)
	return &Runtime{crunPath: crunPath, pastaPath: pastaPath, stateDir: stateDir, log: log}, nil
}

func ensureBinary(dataDir, name, version string, log *slog.Logger) (string, error) {
	binDir := filepath.Join(dataDir, "bin")
	binName := name
	if version != "" {
		binName = fmt.Sprintf("%s-%s", name, version)
	}
	binPath := filepath.Join(binDir, binName)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}

	embedName := name + "-" + arch
	data, err := embedded.Binaries.ReadFile(embedName)
	if err != nil {
		return "", fmt.Errorf("embedded %s binary not found for %s: %w", name, arch, err)
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("creating bin directory: %w", err)
	}

	if err := os.WriteFile(binPath, data, 0755); err != nil {
		return "", fmt.Errorf("extracting %s: %w", name, err)
	}

	log.Info("extracted embedded binary", "name", name, "path", binPath)
	return binPath, nil
}

// Create sets up a container from an OCI bundle without starting it.
func (r *Runtime) Create(id string, bundleDir string) error {
	cmd := r.cmd("create", "--bundle", bundleDir, id)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("crun create %s: %w\n%s", id, err, out)
	}
	return nil
}

// Start starts a previously created container.
func (r *Runtime) Start(id string) error {
	out, err := r.cmd("start", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crun start %s: %w\n%s", id, err, out)
	}
	return nil
}

// ContainerHandle represents a running container managed by crun.
type ContainerHandle struct {
	PID      int
	cmd      *exec.Cmd
	done     chan struct{}
	exitCode int
}

// Wait blocks until the container exits and returns the exit code.
func (h *ContainerHandle) Wait() int {
	<-h.done
	return h.exitCode
}

// Done returns a channel that's closed when the container exits.
func (h *ContainerHandle) Done() <-chan struct{} {
	return h.done
}

// RunContainer starts a container via `crun run` and waits until it's fully running.
// The container's stdout/stderr are attached to the provided writers.
// Returns a ContainerHandle with the container PID available immediately.
func (r *Runtime) RunContainer(id, bundleDir string, stdout, stderr io.Writer) (*ContainerHandle, error) {
	cmd := r.cmd("run", "--bundle", bundleDir, id)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting crun run: %w", err)
	}

	cs, err := r.waitForRunning(id, 10*time.Second)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("container did not start: %w", err)
	}

	h := &ContainerHandle{
		PID:  cs.PID,
		cmd:  cmd,
		done: make(chan struct{}),
	}

	go func() {
		cmd.Wait()
		h.exitCode = cmd.ProcessState.ExitCode()
		close(h.done)
	}()

	return h, nil
}

// Kill sends a signal to the container's init process.
func (r *Runtime) Kill(id string, signal string) error {
	out, err := r.cmd("kill", id, signal).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crun kill %s %s: %w\n%s", id, signal, err, out)
	}
	return nil
}

// Delete removes a container from the runtime state.
func (r *Runtime) Delete(id string, force bool) error {
	args := []string{"delete"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, id)
	out, err := r.cmd(args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crun delete %s: %w\n%s", id, err, out)
	}
	return nil
}

// ContainerState represents the output of `crun state`.
type ContainerState struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "created", "running", "stopped"
	PID    int    `json:"pid"`
}

// State returns the current state of a container.
func (r *Runtime) State(id string) (*ContainerState, error) {
	out, err := r.cmd("state", id).Output()
	if err != nil {
		return nil, fmt.Errorf("crun state %s: %w", id, err)
	}
	var state ContainerState
	if err := json.Unmarshal(out, &state); err != nil {
		return nil, fmt.Errorf("parsing crun state for %s: %w", id, err)
	}
	return &state, nil
}

// Exec runs a command inside a running container.
func (r *Runtime) Exec(id string, cmd []string, env []string) (exitCode int, stdout string, stderr string, err error) {
	args := []string{"exec"}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, id)
	args = append(args, cmd...)

	execCmd := r.cmd(args...)
	var stdoutBuf, stderrBuf strings.Builder
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = &stderrBuf

	runErr := execCmd.Run()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdoutBuf.String(), stderrBuf.String(), nil
		}
		return 1, "", "", fmt.Errorf("crun exec in %s: %w", id, runErr)
	}
	return 0, stdoutBuf.String(), stderrBuf.String(), nil
}

// List returns IDs of all containers known to the runtime.
func (r *Runtime) List() ([]ContainerState, error) {
	out, err := r.cmd("list", "--format", "json").Output()
	if err != nil {
		return nil, fmt.Errorf("crun list: %w", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}
	var containers []ContainerState
	if err := json.Unmarshal(out, &containers); err != nil {
		return nil, fmt.Errorf("parsing crun list: %w", err)
	}
	return containers, nil
}

// CrunPath returns the path to the crun binary.
func (r *Runtime) CrunPath() string { return r.crunPath }

// PastaPath returns the path to the pasta binary.
func (r *Runtime) PastaPath() string { return r.pastaPath }

// StateDir returns the crun state directory (--root flag).
func (r *Runtime) StateDir() string { return r.stateDir }

// PastaInstance represents a running pasta process providing network
// connectivity to a container's network namespace.
type PastaInstance struct {
	cmd *exec.Cmd
	pid int
}

// waitForRunning polls crun state until the container is running and has a PID.
// systemd-run returns before crun has fully created the container, so we need
// to wait before we can attach pasta to the network namespace.
func (r *Runtime) waitForRunning(id string, timeout time.Duration) (*ContainerState, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cs, err := r.State(id)
		if err == nil && cs.PID > 0 {
			return cs, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, fmt.Errorf("container %s did not start within %v", id, timeout)
}

// StartPasta starts a pasta process that provides network connectivity to the
// container's network namespace. Each PortForward maps a host port to a
// container port. The container process sees its default ports; pasta handles
// the forwarding transparently.
func (r *Runtime) StartPasta(containerID string, forwards []PortForward) (*PastaInstance, error) {
	cs, err := r.waitForRunning(containerID, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("waiting for container %s to start: %w", containerID, err)
	}
	return r.StartPastaForPID(containerID, cs.PID, forwards)
}

// StartPastaForPID starts a pasta process attached to a container with a known PID.
// Use this when the container PID is already available (e.g. from RunContainer).
func (r *Runtime) StartPastaForPID(containerID string, pid int, forwards []PortForward) (*PastaInstance, error) {
	args := []string{
		"--ns-ifname", "eth0",
	}

	for _, fwd := range forwards {
		flag := "-t"
		if fwd.Protocol == "udp" {
			flag = "-u"
		}
		if fwd.HostPort == fwd.ContainerPort {
			args = append(args, flag, fmt.Sprintf("%d", fwd.HostPort))
		} else {
			args = append(args, flag, fmt.Sprintf("%d:%d", fwd.HostPort, fwd.ContainerPort))
		}
	}

	args = append(args, fmt.Sprintf("%d", pid))

	cmd := exec.Command(r.pastaPath, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting pasta for %s: %w", containerID, err)
	}

	r.log.Info("pasta started", "container", containerID, "pid", cmd.Process.Pid, "forwards", len(forwards))

	pi := &PastaInstance{cmd: cmd, pid: cmd.Process.Pid}

	go func() {
		cmd.Wait()
		r.log.Debug("pasta exited", "container", containerID, "pid", pi.pid)
	}()

	return pi, nil
}

// Stop terminates the pasta process.
func (p *PastaInstance) Stop() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	p.cmd.Process.Kill()
	p.cmd.Wait()
}

// PortForward describes a host-to-container port mapping via pasta.
type PortForward struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

func (r *Runtime) cmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"--root", r.stateDir}, args...)
	return exec.Command(r.crunPath, fullArgs...)
}
