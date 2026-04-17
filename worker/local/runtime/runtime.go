package runtime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/warsmite/gamejanitor/worker/local/runtime/embedded"
)

const crunVersion = "1.24"

// Runtime wraps crun + pasta for OCI container lifecycle operations.
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

// ContainerHandle represents a running container managed by pasta + crun.
type ContainerHandle struct {
	PID      int
	cmd      *exec.Cmd // the pasta process
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

// Signal sends a signal to the container via the pasta process group.
// The worker process forwards it to crun kill.
func (h *ContainerHandle) Signal(sig syscall.Signal) error {
	if h.cmd == nil || h.cmd.Process == nil {
		return fmt.Errorf("no process")
	}
	// Send to the process group (negative PID) to reach pasta + worker + crun
	return syscall.Kill(-h.cmd.Process.Pid, sig)
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

// RunSync returns a *exec.Cmd for `crun run` that can be executed synchronously.
// Used for short-lived operations (cleanup).
func (r *Runtime) RunSync(id, bundleDir string) *exec.Cmd {
	return r.cmd("run", "--bundle", bundleDir, id)
}

// CrunPath returns the path to the crun binary.
func (r *Runtime) CrunPath() string { return r.crunPath }

// PastaPath returns the path to the pasta binary.
func (r *Runtime) PastaPath() string { return r.pastaPath }

// StateDir returns the crun state directory (--root flag).
func (r *Runtime) StateDir() string { return r.stateDir }

func (r *Runtime) cmd(args ...string) *exec.Cmd {
	fullArgs := append([]string{"--root", r.stateDir}, args...)
	return exec.Command(r.crunPath, fullArgs...)
}

