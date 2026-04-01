package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

// readBwrapChildPID reads the child PID from bwrap's --info-fd JSON output.
// Retries briefly since bwrap writes this asynchronously after startup.
func readBwrapChildPID(infoPath string) int {
	for i := 0; i < 20; i++ {
		data, err := os.ReadFile(infoPath)
		if err == nil && len(data) > 0 {
			var info struct {
				ChildPID int `json:"child-pid"`
			}
			if json.Unmarshal(data, &info) == nil && info.ChildPID > 0 {
				return info.ChildPID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// cleanupOrphanState kills leftover namespace holders, resets failed systemd scopes,
// and unmounts stale overlayfs mounts from a previous gamejanitor crash.
func cleanupOrphanHolders(log *slog.Logger) {
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

// preambleFilterWriter wraps an io.Writer and filters out systemd-run preamble.
// Only filters complete lines at the start of output; passes through everything
// once a non-preamble line is seen.
type preambleFilterWriter struct {
	w           io.Writer
	buf         []byte
	passthrough bool
}

func newPreambleFilterWriter(w io.Writer) *preambleFilterWriter {
	return &preambleFilterWriter{w: w}
}

func (f *preambleFilterWriter) Write(p []byte) (int, error) {
	if f.passthrough {
		return f.w.Write(p)
	}

	f.buf = append(f.buf, p...)
	for {
		idx := bytes.IndexByte(f.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(f.buf[:idx])
		f.buf = f.buf[idx+1:]

		if isSystemdPreamble(line) {
			continue
		}

		// First non-preamble line — flush it and switch to passthrough
		f.passthrough = true
		n, err := f.w.Write([]byte(line + "\n"))
		if err != nil {
			return n, err
		}
		// Write any remaining buffered data
		if len(f.buf) > 0 {
			f.w.Write(f.buf)
			f.buf = nil
		}
		break
	}
	return len(p), nil
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
		if !ok || inst.exited {
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
