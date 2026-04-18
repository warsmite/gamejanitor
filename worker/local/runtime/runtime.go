package runtime

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/warsmite/gamejanitor/worker/local/runtime/embedded"
)

const crunVersion = "1.27"

// Runtime wraps crun + pasta for OCI container lifecycle operations.
// Uses pasta command mode (creates user+net namespaces, execs crun inside).
type Runtime struct {
	crunPath string
	pastaPath string
	stateDir  string
	log       *slog.Logger
}

// New creates a Runtime, extracting embedded binaries if needed.
func New(dataDir string, log *slog.Logger) (*Runtime, error) {
	if os.Getuid() == 0 && !InUserNamespace() {
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

	stateDir := filepath.Join(dataDir, "crun-state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return nil, fmt.Errorf("creating crun state dir: %w", err)
	}

	log.Info("runtime ready", "crun", crunPath, "pasta", pastaPath, "state_dir", stateDir)
	return &Runtime{crunPath: crunPath, pastaPath: pastaPath, stateDir: stateDir, log: log}, nil
}

// InUserNamespace reports whether the current process is inside a user namespace.
// Checks uid_map: the init user namespace maps the full 32-bit UID range (4294967295),
// while a child user namespace maps a limited range.
func InUserNamespace() bool {
	data, err := os.ReadFile("/proc/self/uid_map")
	if err != nil {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(string(data)))
	if len(fields) < 3 {
		return false
	}
	count, err := strconv.Atoi(fields[2])
	if err != nil {
		return false
	}
	return count < 4294967295
}

// extractEmbeddedBinary extracts a binary from the embedded FS to {dataDir}/bin/{binName}.
// Returns (path, extracted) where extracted is true only if the file was written.
func extractEmbeddedBinary(dataDir, name, binName string) (string, bool, error) {
	binDir := filepath.Join(dataDir, "bin")
	binPath := filepath.Join(binDir, binName)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, false, nil
	}

	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}

	data, err := embedded.Binaries.ReadFile(name + "-" + arch)
	if err != nil {
		return "", false, fmt.Errorf("embedded %s binary not found for %s: %w", name, arch, err)
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", false, fmt.Errorf("creating bin directory: %w", err)
	}

	if err := os.WriteFile(binPath, data, 0755); err != nil {
		return "", false, fmt.Errorf("extracting %s: %w", name, err)
	}

	return binPath, true, nil
}

func ensureBinary(dataDir, name, version string, log *slog.Logger) (string, error) {
	binName := name
	if version != "" {
		binName = fmt.Sprintf("%s-%s", name, version)
	}
	path, extracted, err := extractEmbeddedBinary(dataDir, name, binName)
	if err != nil {
		return "", err
	}
	if extracted {
		log.Info("extracted embedded binary", "name", name, "path", path)
	}
	return path, nil
}

// ExtractUserns extracts the embedded userns helper binary to {dataDir}/bin/
// and returns its path. The helper creates a user namespace before exec'ing the
// real binary, giving it CAP_NET_ADMIN for network namespace operations.
func ExtractUserns(dataDir string) (string, error) {
	path, _, err := extractEmbeddedBinary(dataDir, "userns", "userns")
	return path, err
}

// PortForward describes a host-to-container port mapping via pasta.
type PortForward struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

// StartContainer launches a container with crun and sets up networking via pasta.
//
// Uses the OCI two-step lifecycle: crun create (paused, namespaces exist) →
// pasta attaches for port forwarding → crun start (entrypoint runs with network
// ready). Exit detection uses pidfd_open for event-driven notification without
// polling. The pid-file gives us the host PID since crun runs directly.
//
// Requires the process to be inside a user namespace (via the userns helper)
// so that crun has CAP_NET_ADMIN to create network namespaces.
func (r *Runtime) StartContainer(
	id, bundleDir string,
	forwards []PortForward,
	logFile *os.File,
) (*ContainerHandle, error) {
	pidFilePath := filepath.Join(r.stateDir, id+".pid")

	// Step 1: crun create — container paused, namespaces exist, pid-file written.
	// Both stdout and stderr go to the log file (real *os.File, not a Go pipe)
	// so cmd.Run() doesn't block waiting for the container to close pipe FDs.
	// crun errors (e.g. OCI spec issues) also end up in the log file.
	createCmd := r.cmd("create", "--pid-file", pidFilePath, "--bundle", bundleDir, id)
	createCmd.Stdout = logFile
	createCmd.Stderr = logFile
	if err := createCmd.Run(); err != nil {
		// Read the log file tail for the crun error message
		errCtx := ""
		if data, readErr := os.ReadFile(logFile.Name()); readErr == nil && len(data) > 0 {
			errCtx = "\n" + truncateStr(string(data), 500)
		}
		return nil, fmt.Errorf("creating container %s: %w%s", id, err, errCtx)
	}

	containerPID, err := readPIDFile(pidFilePath, 5*time.Second)
	if err != nil {
		r.Delete(id, true)
		return nil, fmt.Errorf("container %s: %w", id, err)
	}

	// Open a pidfd BEFORE starting the container. This gives us event-driven
	// exit notification via epoll/poll without owning the process as a child.
	// pidfd_open works on any visible PID (Linux 5.3+).
	pidfd, err := pidfdOpen(containerPID)
	if err != nil {
		r.Delete(id, true)
		return nil, fmt.Errorf("pidfd_open for container %s (pid %d): %w", id, containerPID, err)
	}

	// Step 2: pasta configures the container's network namespace for port forwarding.
	// In --netns mode, pasta daemonizes (forks to background) after binding ports.
	// The parent returns immediately, so CombinedOutput captures any startup errors.
	//
	// "Address in use" means port forwarding is already active (e.g. from a pasta
	// that survived a gamejanitor restart). This is fine — the old pasta is still
	// forwarding. Any other error is fatal: the container would start without
	// networking.
	if err := r.StartPasta(id, containerPID, forwards); err != nil {
		syscall.Close(pidfd)
		r.Delete(id, true)
		return nil, err
	}

	// Step 3: crun start — unpause the container. Network is ready.
	if out, err := r.cmd("start", id).CombinedOutput(); err != nil {
		syscall.Close(pidfd)
		r.Delete(id, true)
		return nil, fmt.Errorf("starting container %s: %w\n%s", id, err, out)
	}

	h := &ContainerHandle{
		PID:  containerPID,
		done: make(chan struct{}),
		exitCode: -1,
	}

	// Wait for container exit via pidfd (event-driven, no polling).
	go func() {
		pidfdWait(pidfd)
		syscall.Close(pidfd)

		h.exitCode = readProcExitCode(containerPID)
		os.Remove(pidFilePath)
		r.Delete(id, true)
		close(h.done)
	}()

	r.log.Info("container started",
		"container", id,
		"pid", containerPID,
		"forwards", len(forwards),
	)

	return h, nil
}

// readPIDFile polls for a pid-file to appear and returns the PID it contains.
func readPIDFile(path string, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pidStr := strings.TrimSpace(string(data))
			if pid, err := strconv.Atoi(pidStr); err == nil && pid > 0 {
				return pid, nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0, fmt.Errorf("timeout waiting for pid-file %s after %v", path, timeout)
}

// pidfdOpen wraps the pidfd_open(2) syscall (Linux 5.3+).
func pidfdOpen(pid int) (int, error) {
	const SYS_PIDFD_OPEN = 434 // x86_64
	fd, _, errno := syscall.Syscall(SYS_PIDFD_OPEN, uintptr(pid), 0, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// pidfdWait blocks until the process referenced by the pidfd exits.
// Uses ppoll(2) on the pidfd — it becomes readable when the process exits.
func pidfdWait(pidfd int) {
	const POLLIN = 0x0001
	for {
		fds := [1]pollFd{{fd: int32(pidfd), events: POLLIN}}
		// ppoll with 1-second timeout so signals can interrupt
		timeout := syscall.Timespec{Sec: 1}
		n, _, _ := syscall.Syscall6(syscall.SYS_PPOLL,
			uintptr(unsafe.Pointer(&fds[0])), 1,
			uintptr(unsafe.Pointer(&timeout)), 0, 0, 0)
		if n > 0 && fds[0].revents&POLLIN != 0 {
			return
		}
	}
}

type pollFd struct {
	fd      int32
	events  int16
	revents int16
}

// readProcExitCode reads the exit code from /proc/<pid>/stat field 52 (exit_code).
// Returns 0 if the process is already gone or the field can't be parsed.
func readProcExitCode(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	// /proc/pid/stat format: pid (comm) state ... field52=exit_code
	// Fields are space-separated, but comm can contain spaces/parens.
	// Find the closing paren, then split the rest.
	s := string(data)
	idx := strings.LastIndex(s, ") ")
	if idx < 0 {
		return 0
	}
	fields := strings.Fields(s[idx+2:])
	// exit_code is field 52 in /proc/pid/stat, which is index 49 after (comm)
	// (field 1=pid, 2=comm, 3=state, ... so field 52 is at index 49 after comm)
	if len(fields) < 50 {
		return 0
	}
	code, err := strconv.Atoi(fields[49])
	if err != nil {
		return 0
	}
	// exit_code in /proc/pid/stat is the raw wait status
	if code == 0 {
		return 0
	}
	return (code >> 8) & 0xff
}

// --- Container handle ---

// ContainerHandle represents a running container.
type ContainerHandle struct {
	PID      int // container init PID in the host PID namespace
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

// Exec runs a command inside a running container via crun exec.
// Uses the container ID to find the container's namespaces — no PID needed.
// Works on both freshly started and recovered containers as long as crun state exists.
func (r *Runtime) Exec(id string, cmd []string, env []string) (exitCode int, stdout string, stderr string, err error) {
	args := []string{"--root", r.stateDir, "exec"}
	for _, e := range env {
		args = append(args, "--env", e)
	}
	args = append(args, id)
	args = append(args, cmd...)

	execCmd := exec.Command(r.crunPath, args...)

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

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

func (r *Runtime) cmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"--root", r.stateDir}, args...)
	return exec.Command(r.crunPath, fullArgs...)
}

// StartPasta starts pasta for port forwarding into the container's network namespace.
// Pasta daemonizes after binding ports, so this returns once setup is complete.
//
// If a port fails with "Address in use", it means an existing pasta daemon is
// already forwarding that port (e.g. survived a gamejanitor restart). This is
// treated as success — the forwarding is already active. Any other failure is
// returned as an error.
func (r *Runtime) StartPasta(containerID string, containerPID int, forwards []PortForward) error {
	if len(forwards) == 0 {
		return nil
	}

	netNSPath := fmt.Sprintf("/proc/%d/ns/net", containerPID)
	pastaArgs := []string{
		"--netns", netNSPath,
		"--config-net",
		"--ns-ifname", "eth0",
		"--quiet",
	}
	for _, fwd := range forwards {
		flag := "-t"
		if fwd.Protocol == "udp" {
			flag = "-u"
		}
		pastaArgs = append(pastaArgs, flag, fmt.Sprintf("%d:%d", fwd.HostPort, fwd.ContainerPort))
	}

	cmd := exec.Command(r.pastaPath, pastaArgs...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	output := string(out)

	// "Address in use" means an existing pasta is already forwarding this port.
	// This happens after a gamejanitor restart — the old pasta survived and is
	// still working. Check that ALL failures are "Address in use" (not just some).
	lines := strings.Split(strings.TrimSpace(output), "\n")
	allAddressInUse := true
	for _, line := range lines {
		if line == "" || strings.Contains(line, "AVX2") {
			continue
		}
		if !strings.Contains(line, "Address in use") {
			allAddressInUse = false
			break
		}
	}

	if allAddressInUse {
		r.log.Info("port forwarding already active (existing pasta)",
			"container", containerID, "forwards", len(forwards))
		return nil
	}

	return fmt.Errorf("pasta failed for container %s: %w\n%s", containerID, err, output)
}
