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
	"sync"
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

// ContainerHandle represents a running container using the proper OCI lifecycle:
// create (paused) → configure network → start (unpause) → wait for exit.
// Stdout/stderr are captured via pipes that outlive the crun process.
type ContainerHandle struct {
	PID      int
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

// CreateContainer sets up a container from an OCI bundle without starting it.
// The container's namespaces are created and the init process is paused.
// Stdout/stderr are connected via pipes — the container process holds the write
// ends, the returned handle pumps the read ends to the provided writers.
// Call Start() after configuring the network (pasta) to unpause the process.
func (r *Runtime) CreateContainer(id, bundleDir string, stdout, stderr io.Writer) (*ContainerHandle, error) {
	// Create pipes. crun create inherits our pipes → container process gets
	// the write ends via fork+exec. When crun exits after create, the
	// container still holds them. Our goroutine reads from the read ends.
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("creating stdout pipe: %w", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return nil, fmt.Errorf("creating stderr pipe: %w", err)
	}

	cmd := r.cmd("create", "--bundle", bundleDir, id)
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW

	if err := cmd.Run(); err != nil {
		stdoutW.Close()
		stderrW.Close()
		stdoutR.Close()
		stderrR.Close()
		return nil, fmt.Errorf("crun create %s: %w", id, err)
	}
	// Close our write ends — the container process has its own copies
	stdoutW.Close()
	stderrW.Close()

	// Container exists, PID available immediately — no polling
	cs, err := r.State(id)
	if err != nil {
		stdoutR.Close()
		stderrR.Close()
		r.Delete(id, true)
		return nil, fmt.Errorf("getting state after create: %w", err)
	}

	h := &ContainerHandle{
		PID:      cs.PID,
		done:     make(chan struct{}),
		exitCode: -1,
	}

	// Pump container stdio to the caller's writers in background.
	// When the container exits, its write ends close → io.Copy returns EOF.
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { io.Copy(stdout, stdoutR); stdoutR.Close(); wg.Done() }()
		go func() { io.Copy(stderr, stderrR); stderrR.Close(); wg.Done() }()
		wg.Wait()

		// Pipes closed = container exited. Poll for exit code.
		for i := 0; i < 20; i++ {
			cs, err := r.State(id)
			if err != nil || cs.Status == "stopped" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		// Read exit code from the process's waitstatus if available
		if h.PID > 0 {
			h.exitCode = readExitCode(h.PID)
		}

		close(h.done)
	}()

	return h, nil
}

// StartContainer unpauses a previously created container.
func (r *Runtime) StartContainer(id string) error {
	out, err := r.cmd("start", id).CombinedOutput()
	if err != nil {
		return fmt.Errorf("crun start %s: %w\n%s", id, err, out)
	}
	return nil
}

// readExitCode attempts to read the exit code of a process from /proc.
// The process may be a zombie briefly before being reaped.
func readExitCode(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	// /proc/<pid>/stat format: pid (comm) state ... exit_code is field 52
	// but the reliable way is to check if the process is a zombie (state Z)
	// and try to waitpid on it
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return -1
	}
	// If the process is gone, check if crun state has info
	return -1
}

// RunSync returns a *exec.Cmd for `crun run` that can be executed synchronously.
// Used for short-lived operations (cleanup) where the caller wants to
// run the container to completion and capture output.
func (r *Runtime) RunSync(id, bundleDir string) *exec.Cmd {
	return r.cmd("run", "--bundle", bundleDir, id)
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

// StartPasta starts a pasta process that provides network connectivity to a
// pre-created network namespace. The nsPath is a bind-mounted netns file
// created by CreateNetNS.
func (r *Runtime) StartPasta(containerID string, nsPath string, forwards []PortForward) (*PastaInstance, error) {
	args := []string{
		"--netns", nsPath,
		"--config-net",
		"--netns-only",
		"--ns-ifname", "eth0",
		"--quiet",
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
