package testutil

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/warsmite/gamejanitor/worker"
)

// FakeWorker implements worker.Worker with in-memory state tracking.
// Volumes use real temp directories for file operation testing.
// Containers track state transitions and emit events.
type FakeWorker struct {
	mu         sync.Mutex
	volumes    map[string]string            // volume name → temp dir path
	containers map[string]*fakeContainer
	events     chan worker.ContainerEvent
	failures   map[string]error // method name → error to return once
	t          *testing.T
	tmpDir     string // parent dir for volume temp dirs

	// ReadyPattern to echo in logs after container start (simulates game ready).
	// Set this before starting a container to test ready detection.
	ReadyPattern string

	// PulledImages tracks which images have been "pulled".
	PulledImages []string
}

type fakeContainer struct {
	id      string
	name    string
	opts    worker.ContainerOptions
	state   string // "created", "running", "stopped"
	logBuf  bytes.Buffer
}

// NewFakeWorker creates a FakeWorker. Temp directories are cleaned up when the test finishes.
func NewFakeWorker(t *testing.T) *FakeWorker {
	t.Helper()

	tmpDir := t.TempDir()

	fw := &FakeWorker{
		volumes:    make(map[string]string),
		containers: make(map[string]*fakeContainer),
		events:     make(chan worker.ContainerEvent, 64),
		failures:   make(map[string]error),
		t:          t,
		tmpDir:     tmpDir,
	}

	return fw
}

// FailNext causes the next call to the named method to return the given error.
// The failure is consumed on first use (one-shot).
func (w *FakeWorker) FailNext(method string, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.failures[method] = err
}

func (w *FakeWorker) popFailure(method string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err, ok := w.failures[method]; ok {
		delete(w.failures, method)
		return err
	}
	return nil
}

// Container queries

func (w *FakeWorker) GetContainer(id string) *fakeContainer {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.containers[id]
}

func (w *FakeWorker) ContainerCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.containers)
}

func (w *FakeWorker) VolumeCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.volumes)
}

func (w *FakeWorker) VolumeExists(name string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.volumes[name]
	return ok
}

// Worker interface implementation

func (w *FakeWorker) PullImage(ctx context.Context, image string) error {
	if err := w.popFailure("PullImage"); err != nil {
		return err
	}
	w.mu.Lock()
	w.PulledImages = append(w.PulledImages, image)
	w.mu.Unlock()
	return nil
}

func (w *FakeWorker) CreateContainer(ctx context.Context, opts worker.ContainerOptions) (string, error) {
	if err := w.popFailure("CreateContainer"); err != nil {
		return "", err
	}

	id := fmt.Sprintf("fake-%s-%d", opts.Name, time.Now().UnixNano())

	w.mu.Lock()
	w.containers[id] = &fakeContainer{
		id:    id,
		name:  opts.Name,
		opts:  opts,
		state: "created",
	}
	w.mu.Unlock()

	return id, nil
}

func (w *FakeWorker) StartContainer(ctx context.Context, id string) error {
	if err := w.popFailure("StartContainer"); err != nil {
		return err
	}

	w.mu.Lock()
	c, ok := w.containers[id]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("container %s not found", id)
	}
	c.state = "running"

	// Write install marker and ready pattern to log buffer for ReadyWatcher
	c.logBuf.WriteString("[gamejanitor:installed]\n")
	if w.ReadyPattern != "" {
		c.logBuf.WriteString(w.ReadyPattern + "\n")
	}
	w.mu.Unlock()

	// Emit start event
	w.events <- worker.ContainerEvent{
		ContainerID:   id,
		ContainerName: c.name,
		Action:        "start",
	}

	return nil
}

func (w *FakeWorker) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	if err := w.popFailure("StopContainer"); err != nil {
		return err
	}

	w.mu.Lock()
	c, ok := w.containers[id]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("container %s not found", id)
	}
	c.state = "stopped"
	w.mu.Unlock()

	w.events <- worker.ContainerEvent{
		ContainerID:   id,
		ContainerName: c.name,
		Action:        "die",
	}

	return nil
}

func (w *FakeWorker) RemoveContainer(ctx context.Context, id string) error {
	if err := w.popFailure("RemoveContainer"); err != nil {
		return err
	}

	w.mu.Lock()
	delete(w.containers, id)
	w.mu.Unlock()
	return nil
}

func (w *FakeWorker) InspectContainer(ctx context.Context, id string) (*worker.ContainerInfo, error) {
	if err := w.popFailure("InspectContainer"); err != nil {
		return nil, err
	}

	w.mu.Lock()
	c, ok := w.containers[id]
	if !ok {
		w.mu.Unlock()
		return nil, fmt.Errorf("container %s not found", id)
	}
	info := &worker.ContainerInfo{
		ID:        c.id,
		State:     c.state,
		StartedAt: time.Now(),
	}
	w.mu.Unlock()
	return info, nil
}

func (w *FakeWorker) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	if err := w.popFailure("Exec"); err != nil {
		return 1, "", "", err
	}
	return 0, "", "", nil
}

func (w *FakeWorker) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	if err := w.popFailure("ContainerLogs"); err != nil {
		return nil, err
	}

	w.mu.Lock()
	c, ok := w.containers[containerID]
	if !ok {
		w.mu.Unlock()
		return nil, fmt.Errorf("container %s not found", containerID)
	}
	data := c.logBuf.Bytes()
	w.mu.Unlock()

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (w *FakeWorker) ContainerStats(ctx context.Context, containerID string) (*worker.ContainerStats, error) {
	if err := w.popFailure("ContainerStats"); err != nil {
		return nil, err
	}
	return &worker.ContainerStats{
		MemoryUsageMB: 128,
		MemoryLimitMB: 512,
		CPUPercent:    5.0,
	}, nil
}

// Volume operations

func (w *FakeWorker) CreateVolume(ctx context.Context, name string) error {
	if err := w.popFailure("CreateVolume"); err != nil {
		return err
	}

	dir := filepath.Join(w.tmpDir, "volumes", name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating volume dir: %w", err)
	}

	w.mu.Lock()
	w.volumes[name] = dir
	w.mu.Unlock()
	return nil
}

func (w *FakeWorker) RemoveVolume(ctx context.Context, name string) error {
	if err := w.popFailure("RemoveVolume"); err != nil {
		return err
	}

	w.mu.Lock()
	dir, ok := w.volumes[name]
	if ok {
		delete(w.volumes, name)
	}
	w.mu.Unlock()

	if ok {
		os.RemoveAll(dir)
	}
	return nil
}

func (w *FakeWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	if err := w.popFailure("VolumeSize"); err != nil {
		return 0, err
	}

	w.mu.Lock()
	dir, ok := w.volumes[volumeName]
	w.mu.Unlock()

	if !ok {
		return 0, fmt.Errorf("volume %s not found", volumeName)
	}

	var size int64
	filepath.Walk(dir, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, nil
}

// Volume file operations — use real filesystem in temp dirs

func (w *FakeWorker) volumePath(volumeName, path string) (string, error) {
	w.mu.Lock()
	dir, ok := w.volumes[volumeName]
	w.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("volume %s not found", volumeName)
	}
	return filepath.Join(dir, filepath.Clean(path)), nil
}

func (w *FakeWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	if err := w.popFailure("ListFiles"); err != nil {
		return nil, err
	}

	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(fullPath)
	if err != nil {
		return nil, err
	}

	var files []worker.FileEntry
	for _, e := range entries {
		info, _ := e.Info()
		if info == nil {
			continue
		}
		files = append(files, worker.FileEntry{
			Name:    e.Name(),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	return files, nil
}

func (w *FakeWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	if err := w.popFailure("ReadFile"); err != nil {
		return nil, err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(fullPath)
}

func (w *FakeWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	if err := w.popFailure("WriteFile"); err != nil {
		return err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, content, perm)
}

func (w *FakeWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	if err := w.popFailure("DeletePath"); err != nil {
		return err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(fullPath)
}

func (w *FakeWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	if err := w.popFailure("CreateDirectory"); err != nil {
		return err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return err
	}
	return os.MkdirAll(fullPath, 0755)
}

func (w *FakeWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	if err := w.popFailure("RenamePath"); err != nil {
		return err
	}
	fromPath, err := w.volumePath(volumeName, from)
	if err != nil {
		return err
	}
	toPath, err := w.volumePath(volumeName, to)
	if err != nil {
		return err
	}
	return os.Rename(fromPath, toPath)
}

// Copy operations

func (w *FakeWorker) CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error) {
	if err := w.popFailure("CopyFromContainer"); err != nil {
		return nil, err
	}
	return []byte{}, nil
}

func (w *FakeWorker) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	if err := w.popFailure("CopyToContainer"); err != nil {
		return err
	}
	return nil
}

func (w *FakeWorker) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	if err := w.popFailure("CopyDirFromContainer"); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (w *FakeWorker) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	if err := w.popFailure("CopyTarToContainer"); err != nil {
		return err
	}
	return nil
}

// Backup/restore operations

func (w *FakeWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if err := w.popFailure("BackupVolume"); err != nil {
		return nil, err
	}
	// Return a small valid tar.gz-ish blob. Real tests that need actual tar content
	// should use the worker integration tests with Docker.
	return io.NopCloser(bytes.NewReader([]byte("fake-backup-data"))), nil
}

func (w *FakeWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	if err := w.popFailure("RestoreVolume"); err != nil {
		return err
	}
	// Drain the reader to simulate restore
	io.Copy(io.Discard, tarStream)
	return nil
}

// Events

func (w *FakeWorker) WatchEvents(ctx context.Context) (<-chan worker.ContainerEvent, <-chan error) {
	errCh := make(chan error, 1)
	outCh := make(chan worker.ContainerEvent, 64)

	go func() {
		defer close(outCh)
		for {
			select {
			case <-ctx.Done():
				return
			case evt := <-w.events:
				select {
				case outCh <- evt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return outCh, errCh
}

// Game scripts

func (w *FakeWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	if err := w.popFailure("PrepareGameScripts"); err != nil {
		return "", "", err
	}

	scriptDir := filepath.Join(w.tmpDir, "scripts", gameserverID)
	if err := os.MkdirAll(scriptDir, 0755); err != nil {
		return "", "", err
	}
	return scriptDir, "", nil
}

// Compile-time check that FakeWorker implements worker.Worker.
var _ worker.Worker = (*FakeWorker)(nil)
