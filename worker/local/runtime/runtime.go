package runtime

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/worker/local/runtime/embedded"
)

const crunVersion = "1.24"

// Runtime wraps crun + pasta for OCI container lifecycle operations.
// Uses pasta command mode (creates user+net namespaces, execs crun inside).
type Runtime struct {
	crunPath    string
	pastaPath   string
	nsenterPath string
	stateDir    string
	log         *slog.Logger
}

// New creates a Runtime, extracting embedded binaries if needed.
func New(dataDir string, log *slog.Logger) (*Runtime, error) {
	if os.Getuid() == 0 {
		return nil, fmt.Errorf("gamejanitor must not run as root — create a dedicated user (e.g. useradd -r -m gamejanitor)")
	}

	crunPath, err := ensureBinary(dataDir, "crun", crunVersion, log)
	if err != nil {
		return nil, fmt.Errorf("crun not available: %w", err)
	}

	pastaPath, err := ensureBinary(dataDir, "pasta", "", log)
	if err != nil {
		return nil, fmt.Errorf("pasta not available: %w", err)
	}

	nsenterPath := findNsenter()
	if nsenterPath == "" {
		return nil, fmt.Errorf("nsenter not found (required for container exec) — install util-linux")
	}

	stateDir := filepath.Join(dataDir, "crun-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("creating crun state dir: %w", err)
	}

	log.Info("runtime ready", "crun", crunPath, "pasta", pastaPath, "nsenter", nsenterPath, "state_dir", stateDir)
	return &Runtime{crunPath: crunPath, pastaPath: pastaPath, nsenterPath: nsenterPath, stateDir: stateDir, log: log}, nil
}

// findNsenter locates the nsenter binary, checking PATH and common system locations.
func findNsenter() string {
	if p, err := exec.LookPath("nsenter"); err == nil {
		return p
	}
	for _, p := range []string{
		"/run/current-system/sw/bin/nsenter", // NixOS
		"/usr/bin/nsenter",
		"/bin/nsenter",
		"/usr/sbin/nsenter",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
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

// PortForward describes a host-to-container port mapping via pasta.
type PortForward struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

// StartContainer launches a container inside a pasta-managed network namespace.
// pasta command mode creates user+net namespaces and execs crun inside them.
func (r *Runtime) StartContainer(
	id, bundleDir string,
	forwards []PortForward,
	stdout, stderr io.Writer,
) (*ContainerHandle, error) {
	args := []string{
		"--config-net",
		"--ns-ifname", "eth0",
		"--quiet",
	}
	for _, fwd := range forwards {
		flag := "-t"
		if fwd.Protocol == "udp" {
			flag = "-u"
		}
		args = append(args, flag, fmt.Sprintf("%d:%d", fwd.HostPort, fwd.ContainerPort))
	}

	args = append(args, "--",
		r.crunPath, "--root", r.stateDir,
		"run", "--bundle", bundleDir, id,
	)

	cmd := exec.Command(r.pastaPath, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting container %s: %w", id, err)
	}

	pastaPID := cmd.Process.Pid

	cmdDone := make(chan struct{})
	go func() {
		cmd.Wait()
		close(cmdDone)
	}()

	// Walk the process tree (pasta → crun → container init) to find the
	// container init's host PID. crun's pid-file contains the PID inside
	// an intermediate PID namespace, which is useless from outside.
	containerPID, err := waitForContainerPID(pastaPID, 10*time.Second, cmdDone)
	if err != nil {
		// Short-lived containers may exit before the process tree is walked.
		select {
		case <-cmdDone:
			h := &ContainerHandle{
	
				cmd:      cmd,
				done:     make(chan struct{}),
				exitCode: cmd.ProcessState.ExitCode(),
			}
			close(h.done)
			r.log.Info("short-lived container exited before PID lookup",
				"container", id, "exit_code", h.exitCode)
			return h, nil
		default:
			syscall.Kill(-pastaPID, syscall.SIGKILL)
			<-cmdDone
			return nil, fmt.Errorf("container %s failed to start: %w", id, err)
		}
	}

	h := &ContainerHandle{
		PID:  containerPID,
		cmd:  cmd,
		done:     make(chan struct{}),
		exitCode: -1,
	}

	go func() {
		<-cmdDone
		h.exitCode = cmd.ProcessState.ExitCode()
		close(h.done)
	}()

	r.log.Info("container started",
		"container", id,
		"pid", containerPID,
		"pasta_pid", pastaPID,
		"forwards", len(forwards),
	)

	return h, nil
}

// waitForContainerPID polls the process tree to find the container init's host
// PID. The tree is: pasta → crun → container init.
func waitForContainerPID(pastaPID int, timeout time.Duration, earlyExit <-chan struct{}) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-earlyExit:
			return 0, fmt.Errorf("process exited before container started")
		default:
		}
		crunPID, err := readChildPID(pastaPID)
		if err == nil {
			containerPID, err := readChildPID(crunPID)
			if err == nil {
				return containerPID, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("timeout waiting for container process after %v", timeout)
}

func readChildPID(parentPID int) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", parentPID, parentPID))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) == 0 {
		return 0, fmt.Errorf("no children for pid %d", parentPID)
	}
	return strconv.Atoi(fields[0])
}

// --- Container handle ---

// ContainerHandle represents a running container managed by pasta + crun.
type ContainerHandle struct {
	PID      int       // container init PID in the host PID namespace
	cmd      *exec.Cmd // the pasta process (nil for recovered containers)
	done     chan struct{}
	exitCode int
}

// NewRecoveredHandle creates a ContainerHandle for a container that survived
// a gamejanitor restart.
func NewRecoveredHandle(containerPID int) *ContainerHandle {
	return &ContainerHandle{
		PID:      containerPID,
		done:     make(chan struct{}),
		exitCode: -1,
	}
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

// Signal sends a signal directly to the container's init process.
// Works across user namespace boundaries because the host user owns the process.
func (h *ContainerHandle) Signal(sig syscall.Signal) error {
	if h.PID <= 0 {
		return fmt.Errorf("no container PID")
	}
	return syscall.Kill(h.PID, sig)
}

// --- Exec ---

// Exec runs a command inside a running container by entering its namespaces
// via nsenter. We open the namespace fds ourselves (as the container init's
// ancestor, satisfying Yama ptrace_scope=1) and pass them to nsenter via
// ExtraFiles, using the resolved nsenter path from init.
func (r *Runtime) Exec(id string, cmd []string, env []string, containerPID int) (exitCode int, stdout string, stderr string, err error) {
	nsTypes := []string{"user", "net", "mnt", "ipc", "uts"}
	var files []*os.File
	closeFiles := func() {
		for _, f := range files {
			f.Close()
		}
	}

	var nsArgs []string
	for _, ns := range nsTypes {
		f, openErr := os.Open(fmt.Sprintf("/proc/%d/ns/%s", containerPID, ns))
		if openErr != nil {
			closeFiles()
			return 1, "", "", fmt.Errorf("opening %s namespace for pid %d: %w", ns, containerPID, openErr)
		}
		fd := 3 + len(files) // ExtraFiles start at fd 3
		files = append(files, f)

		flag := map[string]string{"user": "-U", "net": "-n", "mnt": "-m", "ipc": "-i", "uts": "-u"}[ns]
		nsArgs = append(nsArgs, fmt.Sprintf("%s=/proc/self/fd/%d", flag, fd))
	}
	defer closeFiles()

	nsArgs = append(nsArgs, "--preserve-credentials", "--")
	nsArgs = append(nsArgs, cmd...)

	execCmd := exec.Command(r.nsenterPath, nsArgs...)
	execCmd.ExtraFiles = files
	if len(env) > 0 {
		execCmd.Env = env
	}

	var stdoutBuf, stderrBuf strings.Builder
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = &stderrBuf

	runErr := execCmd.Run()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return exitErr.ExitCode(), stdoutBuf.String(), stderrBuf.String(), nil
		}
		return 1, "", "", fmt.Errorf("exec in %s: %w", id, runErr)
	}
	return 0, stdoutBuf.String(), stderrBuf.String(), nil
}

// --- Direct crun operations ---

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

// RunSync returns a *exec.Cmd for `crun run` that can be executed synchronously.
// Used for short-lived operations (cleanup containers that don't need networking).
func (r *Runtime) RunSync(id, bundleDir string) *exec.Cmd {
	return r.cmd("run", "--bundle", bundleDir, id)
}

func (r *Runtime) cmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"--root", r.stateDir}, args...)
	return exec.Command(r.crunPath, fullArgs...)
}
