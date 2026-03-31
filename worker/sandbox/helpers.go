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

// cleanupOrphanHolders kills any leftover namespace holder processes from a previous
// gamejanitor crash. Scans /proc for "unshare" processes with "sleep infinity".
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
	if killed > 0 {
		log.Warn("killed orphaned namespace holders from previous run", "count", killed)
	}
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

// ioStringReader wraps a string as an io.Reader.
type ioStringReader string

func (s ioStringReader) Read(p []byte) (int, error) {
	return strings.NewReader(string(s)).Read(p)
}

// tailFile reads the last n lines from a file.
func tailFile(f *os.File, n int) ([]string, error) {
	f.Seek(0, io.SeekStart)
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n && n > 0 {
		lines = lines[len(lines)-n:]
	}
	return lines, scanner.Err()
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

