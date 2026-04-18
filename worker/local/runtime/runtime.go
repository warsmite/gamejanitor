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

	"encoding/json"

	"github.com/warsmite/gamejanitor/worker/local/runtime/embedded"
	"golang.org/x/sys/unix"
)

const crunVersion = "1.27"

// Runtime wraps crun + pasta for OCI container lifecycle operations.
type Runtime struct {
	crunPath string
	pastaPath string
	stateDir  string
	log       *slog.Logger
}

// New creates a Runtime, extracting embedded binaries if needed.
// Sets PR_SET_CHILD_SUBREAPER so orphaned container processes are reparented
// to this process, enabling waitid to collect their exit status.
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
// ready). The container PID is read from crun's status file (host PID).
// Exit detection uses pidfd poll; exit code from crun's status file.
//
// Requires the process to be inside a user namespace (via the userns helper)
// so that crun has CAP_NET_ADMIN to create network namespaces.
func (r *Runtime) StartContainer(
	id, bundleDir string,
	forwards []PortForward,
	logFile *os.File,
	exitCodePath string,
) (*ContainerHandle, error) {
	pastaPIDPath := filepath.Join(r.stateDir, id+".pasta.pid")

	// Step 1: crun create — container paused, namespaces exist.
	createCmd := r.cmd("create", "--bundle", bundleDir, id)
	createCmd.Stdout = logFile
	createCmd.Stderr = logFile
	if err := createCmd.Run(); err != nil {
		errCtx := ""
		if data, readErr := os.ReadFile(logFile.Name()); readErr == nil && len(data) > 0 {
			errCtx = "\n" + truncateStr(string(data), 500)
		}
		return nil, fmt.Errorf("creating container %s: %w%s", id, err, errCtx)
	}

	// Read the container init's host PID from crun's status file.
	containerPID, err := r.readContainerPID(id)
	if err != nil {
		r.Delete(id, true)
		return nil, fmt.Errorf("container %s: %w", id, err)
	}

	// Open a pidfd for event-driven exit notification.
	pidfd, err := unix.PidfdOpen(containerPID, 0)
	if err != nil {
		r.Delete(id, true)
		return nil, fmt.Errorf("pidfd_open for container %s (pid %d): %w", id, containerPID, err)
	}

	// Step 2: pasta configures port forwarding in the container's network namespace.
	pastaPID, pastaErr := r.StartPasta(id, containerPID, forwards, pastaPIDPath)
	if pastaErr != nil {
		unix.Close(pidfd)
		r.Delete(id, true)
		return nil, pastaErr
	}

	// Step 3: crun start — unpause the container. Network is ready.
	if out, err := r.cmd("start", id).CombinedOutput(); err != nil {
		unix.Close(pidfd)
		killPasta(pastaPID)
		r.Delete(id, true)
		return nil, fmt.Errorf("starting container %s: %w\n%s", id, err, out)
	}

	h := &ContainerHandle{
		PID:      containerPID,
		PastaPID: pastaPID,
		done:     make(chan struct{}),
		exitCode: -1,
	}

	// Wait for container exit via pidfd (event-driven, no polling).
	go func() {
		pollPidfd(pidfd)
		unix.Close(pidfd)

		// Read exit code written by the entrypoint wrapper before cleanup.
		h.exitCode = readExitCodeFile(exitCodePath)

		killPasta(h.PastaPID)
		os.Remove(pastaPIDPath)
		os.Remove(exitCodePath)
		r.Delete(id, true)
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

// readContainerPID reads the container init's host PID from crun's status file.
func (r *Runtime) readContainerPID(id string) (int, error) {
	statusPath := filepath.Join(r.stateDir, id, "status")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		return 0, fmt.Errorf("reading crun status: %w", err)
	}
	var status struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		return 0, fmt.Errorf("parsing crun status: %w", err)
	}
	if status.PID <= 0 {
		return 0, fmt.Errorf("invalid PID in crun status: %d", status.PID)
	}
	return status.PID, nil
}

func readExitCodeFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return -1
	}
	return code
}

// pollPidfd blocks until the pidfd becomes readable (process exited).
func pollPidfd(pidfd int) {
	for {
		fds := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
		n, _ := unix.Poll(fds, 1000)
		if n > 0 && fds[0].Revents&unix.POLLIN != 0 {
			return
		}
	}
}

// killPasta sends SIGTERM to a pasta daemon process if it's still alive.
func killPasta(pid int) {
	if pid <= 0 {
		return
	}
	syscall.Kill(pid, syscall.SIGTERM)
}

// --- Container handle ---

// ContainerHandle represents a running container.
type ContainerHandle struct {
	PID      int // container init PID in the host PID namespace
	PastaPID int // pasta daemon PID for port forwarding (0 if no ports)
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
// Returns the pasta daemon PID (0 if no forwards or on "Address in use" reuse).
//
// If a port fails with "Address in use", it means an existing pasta daemon is
// already forwarding that port (e.g. survived a gamejanitor restart). This is
// treated as success — the forwarding is already active. Any other failure is
// returned as an error.
func (r *Runtime) StartPasta(containerID string, containerPID int, forwards []PortForward, pidPath string) (int, error) {
	if len(forwards) == 0 {
		return 0, nil
	}

	netNSPath := fmt.Sprintf("/proc/%d/ns/net", containerPID)
	pastaArgs := []string{
		"--netns", netNSPath,
		"--config-net",
		"--ns-ifname", "eth0",
		"--quiet",
		"-P", pidPath,
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
		// Read pasta PID from its pid file
		pid, _ := readPIDFile(pidPath, 1*time.Second)
		return pid, nil
	}

	output := string(out)

	// "Address in use" means an existing pasta is already forwarding this port.
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
		// Try to read PID from a previous run's pid file
		existingPID, _ := readPIDFile(pidPath, 100*time.Millisecond)
		r.log.Info("port forwarding already active (existing pasta)",
			"container", containerID, "forwards", len(forwards), "pasta_pid", existingPID)
		return existingPID, nil
	}

	return 0, fmt.Errorf("pasta failed for container %s: %w\n%s", containerID, err, output)
}
