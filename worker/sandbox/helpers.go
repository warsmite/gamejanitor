package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// cleanupOrphanState kills leftover namespace holders, resets failed systemd scopes,
// and unmounts stale overlayfs mounts from a previous gamejanitor crash.
// Holders belonging to active gj-*.scope units are preserved for recovery.
func cleanupOrphanHolders(log *slog.Logger) {
	// Collect active gj-* scope PIDs so we don't kill recoverable holders
	activeScopePIDs := activeGJScopePIDs()

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return
	}
	killed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}
		// cmdline is null-separated
		cmd := strings.ReplaceAll(string(cmdline), "\x00", " ")
		if strings.Contains(cmd, "unshare") && strings.Contains(cmd, "sleep infinity") {
			if activeScopePIDs[pid] {
				continue
			}
			syscall.Kill(pid, syscall.SIGKILL)
			killed++
		}
	}
	// Also reset any failed systemd scopes from previous runs
	if out, err := exec.Command("sh", "-c", "systemctl list-units --type=scope --state=failed --no-legend --plain 2>/dev/null | grep gj- | awk '{print $1}'").Output(); err == nil {
		for _, unit := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if unit != "" {
				exec.Command("systemctl", "reset-failed", unit).Run()
				killed++
			}
		}
	}
	// Same for user scopes
	if out, err := exec.Command("sh", "-c", "systemctl --user list-units --type=scope --state=failed --no-legend --plain 2>/dev/null | grep gj- | awk '{print $1}'").Output(); err == nil {
		for _, unit := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if unit != "" {
				exec.Command("systemctl", "--user", "reset-failed", unit).Run()
				killed++
			}
		}
	}

	if killed > 0 {
		log.Warn("cleaned up orphaned sandbox state from previous run", "count", killed)
	}
}

// activeGJScopePIDs returns a set of PIDs that belong to active gj-*.scope cgroups.
// Used to avoid killing holder processes that are part of recoverable instances.
func activeGJScopePIDs() map[int]bool {
	pids := make(map[int]bool)

	// Try both system and user scopes
	for _, userFlag := range [][]string{nil, {"--user"}} {
		args := append(userFlag, "list-units", "--type=scope", "--state=active", "--no-legend", "--plain")
		out, err := exec.Command("systemctl", args...).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || !strings.HasPrefix(fields[0], "gj-") {
				continue
			}
			showArgs := append(userFlag, "show", "-p", "ControlGroup", fields[0])
			cgOut, err := exec.Command("systemctl", showArgs...).Output()
			if err != nil {
				continue
			}
			cgPath := strings.TrimPrefix(strings.TrimSpace(string(cgOut)), "ControlGroup=")
			if cgPath == "" {
				continue
			}
			data, err := os.ReadFile("/sys/fs/cgroup" + cgPath + "/cgroup.procs")
			if err != nil {
				continue
			}
			for _, pidLine := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				pid, err := strconv.Atoi(strings.TrimSpace(pidLine))
				if err == nil && pid > 0 {
					pids[pid] = true
				}
			}
		}
	}
	return pids
}

// cleanupOverlayMounts unmounts any stale overlayfs mounts under dataDir/images.
// These can linger if gamejanitor crashes without a clean shutdown.
func cleanupOverlayMounts(dataDir string, log *slog.Logger) {
	mountsData, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return
	}
	imagesDir := filepath.Join(dataDir, "images")
	unmounted := 0
	for _, line := range strings.Split(string(mountsData), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[2] != "overlay" {
			continue
		}
		mountpoint := fields[1]
		if strings.HasPrefix(mountpoint, imagesDir) {
			if err := syscall.Unmount(mountpoint, 0); err == nil {
				unmounted++
			}
		}
	}
	if unmounted > 0 {
		log.Warn("unmounted stale overlayfs mounts", "count", unmounted)
	}
}

// truncate returns the last n bytes of a string.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// safeBuffer is a bytes.Buffer safe for concurrent stdout/stderr capture.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}


// tailFile reads the last n lines from a file, filtering out sandbox preamble.
func tailFile(f *os.File, n int) ([]string, error) {
	f.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		line := scanner.Text()
		if isSystemdPreamble(line) {
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) > n && n > 0 {
		lines = lines[len(lines)-n:]
	}
	return lines, scanner.Err()
}

// isSystemdPreamble returns true for systemd-run output lines that
// shouldn't be shown as game server logs.
func isSystemdPreamble(line string) bool {
	return strings.HasPrefix(line, "Running as unit: ")
}

const (
	logMaxBytes   = 50 * 1024 * 1024 // 50MB per log file
	logMaxBackups = 1                 // keep 1 rotated file (output.log.0)
)

// rotatingWriter wraps a log file with size-based rotation. When the file
// exceeds logMaxBytes, it's renamed to output.log.0 and a new file is created.
type rotatingWriter struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	written int64
}

func newRotatingWriter(path string) (*rotatingWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	return &rotatingWriter{f: f, path: path}, nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.written+int64(len(p)) > logMaxBytes {
		w.rotate()
	}

	n, err := w.f.Write(p)
	w.written += int64(n)
	return n, err
}

func (w *rotatingWriter) rotate() {
	w.f.Close()
	os.Remove(w.path + ".0")
	os.Rename(w.path, w.path+".0")
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return
	}
	w.f = f
	w.written = 0
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// followReader wraps a file for tailing with follow support.
type followReader struct {
	f          *os.File
	ctx        context.Context
	instanceID string
	worker     *SandboxWorker
}

func newFollowReader(ctx context.Context, f *os.File, instanceID string, w *SandboxWorker) *followReader {
	return &followReader{f: f, ctx: ctx, instanceID: instanceID, worker: w}
}

func (r *followReader) Read(p []byte) (int, error) {
	for {
		n, err := r.f.Read(p)
		if n > 0 {
			return n, nil
		}
		if err != nil && err != io.EOF {
			return 0, err
		}

		// Check if instance is still running
		r.worker.mu.Lock()
		inst, ok := r.worker.instances[r.instanceID]
		r.worker.mu.Unlock()
		if !ok || inst.exited.Load() {
			return 0, io.EOF
		}

		// Check context
		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-time.After(250 * time.Millisecond):
			// Poll again
		}
	}
}

func (r *followReader) Close() error {
	return r.f.Close()
}

// isInsideDir returns true if path is inside dir after cleaning both paths.
// Unlike the deprecated filepath.HasPrefix, this properly handles edge cases
// like /tmp/evil-prefix matching /tmp/evil.
func isInsideDir(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != ".."
}
