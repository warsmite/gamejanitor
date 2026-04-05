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

	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// FakeWorker implements worker.Worker with in-memory state tracking.
// Volumes use real temp directories for file operation testing.
// Instances track state transitions and emit events.
type FakeWorker struct {
	mu         sync.Mutex
	volumes    map[string]string            // volume name → temp dir path
	instances map[string]*fakeInstance
	events     chan worker.InstanceStateUpdate
	failures   map[string]error // method name → error to return once
	t          *testing.T
	tmpDir     string // parent dir for volume temp dirs

	// ReadyPattern to echo in logs after instance start (simulates game ready).
	// Set this before starting a instance to test ready detection.
	ReadyPattern string

	// PulledImages tracks which images have been "pulled".
	PulledImages []string
}

type fakeInstance struct {
	id     string
	name   string
	opts   worker.InstanceOptions
	state  string // "created", "running", "stopped"
	logBuf bytes.Buffer
}

// NewFakeWorker creates a FakeWorker. Temp directories are cleaned up when the test finishes.
func NewFakeWorker(t *testing.T) *FakeWorker {
	t.Helper()

	tmpDir := t.TempDir()

	fw := &FakeWorker{
		volumes:    make(map[string]string),
		instances: make(map[string]*fakeInstance),
		events:     make(chan worker.InstanceStateUpdate, 64),
		failures:   make(map[string]error),
		t:          t,
		tmpDir:     tmpDir,
	}

	return fw
}

// AddFakeInstance injects a running instance into the FakeWorker without going
// through the full Start lifecycle. Used in tests that need a instance to exist
// without triggering lifecycle events.
func (w *FakeWorker) AddFakeInstance(gameserverID string) string {
	id := fmt.Sprintf("fake-%s-%d", gameserverID, time.Now().UnixNano())
	w.mu.Lock()
	w.instances[id] = &fakeInstance{
		id:    id,
		name:  naming.InstanceName(gameserverID),
		state: "running",
	}
	w.mu.Unlock()
	return id
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

// Instance queries

func (w *FakeWorker) GetInstance(id string) *fakeInstance {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.instances[id]
}

func (w *FakeWorker) InstanceCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.instances)
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

func (w *FakeWorker) PullImage(ctx context.Context, image string, onProgress func(worker.PullProgress)) error {
	if err := w.popFailure("PullImage"); err != nil {
		return err
	}
	w.mu.Lock()
	w.PulledImages = append(w.PulledImages, image)
	w.mu.Unlock()
	return nil
}

func (w *FakeWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
	if err := w.popFailure("CreateInstance"); err != nil {
		return "", err
	}

	id := fmt.Sprintf("fake-%s-%d", opts.Name, time.Now().UnixNano())

	w.mu.Lock()
	w.instances[id] = &fakeInstance{
		id:    id,
		name:  opts.Name,
		opts:  opts,
		state: "created",
	}
	w.mu.Unlock()

	return id, nil
}

func (w *FakeWorker) StartInstance(ctx context.Context, id string, readyPattern string) error {
	if err := w.popFailure("StartInstance"); err != nil {
		return err
	}

	w.mu.Lock()
	c, ok := w.instances[id]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("instance %s not found", id)
	}

	// Short-lived instances (install/update) run to completion and exit.
	// Detected by entrypoint override — these run a script and exit rather
	// than staying alive as a long-running game server.
	isShortLived := len(c.opts.Entrypoint) > 0 && readyPattern == ""
	if isShortLived {
		c.state = "exited"
		w.mu.Unlock()
		return nil
	}

	c.state = "running"
	if w.ReadyPattern != "" {
		c.logBuf.WriteString(w.ReadyPattern + "\n")
	}
	w.mu.Unlock()

	// Emit running state update
	w.events <- worker.InstanceStateUpdate{
		InstanceID:   id,
		InstanceName: c.name,
		State:        worker.StateRunning,
	}

	return nil
}

func (w *FakeWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	if err := w.popFailure("StopInstance"); err != nil {
		return err
	}

	w.mu.Lock()
	c, ok := w.instances[id]
	if !ok {
		w.mu.Unlock()
		return fmt.Errorf("instance %s not found", id)
	}
	c.state = "stopped"
	w.mu.Unlock()

	// No "die" event here. In a real runtime, exit events are emitted for all container
	// exits (expected and unexpected). StatusManager distinguishes them by checking
	// if the gameserver is in "stopping" state. But because the lifecycle publishes
	// InstanceStoppingEvent and the StatusSubscriber processes it asynchronously,
	// there's a race window where "die" arrives before the subscriber has set
	// "stopping" — causing a false "unexpected death" error.
	//
	// The lifecycle code handles expected stops by publishing InstanceStoppedEvent
	// directly. The "die" event path is only needed for crash detection (unexpected
	// exits). Use SimulateCrash() to test that path explicitly.

	return nil
}

// SimulateCrash emits a "die" event as if the instance crashed unexpectedly.
// Use this to test auto-restart and crash detection, not for expected stops.
func (w *FakeWorker) SimulateCrash(instanceID string) {
	w.mu.Lock()
	c, ok := w.instances[instanceID]
	if ok {
		c.state = "stopped"
	}
	w.mu.Unlock()

	w.events <- worker.InstanceStateUpdate{
		InstanceID:   instanceID,
		InstanceName: c.name,
		State:        worker.StateExited,
	}
}

func (w *FakeWorker) RemoveInstance(ctx context.Context, id string) error {
	if err := w.popFailure("RemoveInstance"); err != nil {
		return err
	}

	w.mu.Lock()
	delete(w.instances, id)
	w.mu.Unlock()
	return nil
}

func (w *FakeWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
	if err := w.popFailure("InspectInstance"); err != nil {
		return nil, err
	}

	w.mu.Lock()
	c, ok := w.instances[id]
	if !ok {
		w.mu.Unlock()
		return nil, fmt.Errorf("instance %s not found", id)
	}
	info := &worker.InstanceInfo{
		ID:        c.id,
		State:     c.state,
		StartedAt: time.Now(),
	}
	w.mu.Unlock()
	return info, nil
}

func (w *FakeWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	if err := w.popFailure("Exec"); err != nil {
		return 1, "", "", err
	}
	return 0, "", "", nil
}

func (w *FakeWorker) InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error) {
	if err := w.popFailure("InstanceLogs"); err != nil {
		return nil, err
	}

	w.mu.Lock()
	c, ok := w.instances[instanceID]
	if !ok {
		w.mu.Unlock()
		return nil, fmt.Errorf("instance %s not found", instanceID)
	}
	data := make([]byte, c.logBuf.Len())
	copy(data, c.logBuf.Bytes())
	w.mu.Unlock()

	return io.NopCloser(bytes.NewReader(data)), nil
}

func (w *FakeWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
	if err := w.popFailure("InstanceStats"); err != nil {
		return nil, err
	}
	return &worker.InstanceStats{
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

func (w *FakeWorker) resolve(_ context.Context, volumeName string) (string, error) {
	w.mu.Lock()
	dir, ok := w.volumes[volumeName]
	w.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("volume %s not found", volumeName)
	}
	return dir, nil
}

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

func (w *FakeWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	if err := w.popFailure("OpenFile"); err != nil {
		return nil, 0, err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, err
	}
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, stat.Size(), nil
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

func (w *FakeWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	if err := w.popFailure("WriteFileStream"); err != nil {
		return err
	}
	fullPath, err := w.volumePath(volumeName, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(fullPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, reader); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (w *FakeWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	if err := w.popFailure("DownloadFile"); err != nil {
		return err
	}
	fullPath, err := w.volumePath(volumeName, destPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	// In tests, write a placeholder — no actual HTTP download
	return os.WriteFile(fullPath, []byte("downloaded:"+url), 0644)
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

func (w *FakeWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
	if err := w.popFailure("CopyFromInstance"); err != nil {
		return nil, err
	}
	return []byte{}, nil
}

func (w *FakeWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
	if err := w.popFailure("CopyToInstance"); err != nil {
		return err
	}
	return nil
}

func (w *FakeWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	if err := w.popFailure("CopyDirFromInstance"); err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (w *FakeWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	if err := w.popFailure("CopyTarToInstance"); err != nil {
		return err
	}
	return nil
}

// Backup/restore operations

func (w *FakeWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	if err := w.popFailure("BackupVolume"); err != nil {
		return nil, err
	}
	return worker.BackupVolumeDirect(w.resolve, ctx, volumeName)
}

func (w *FakeWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	if err := w.popFailure("RestoreVolume"); err != nil {
		return err
	}
	return worker.RestoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
}

// Events

func (w *FakeWorker) WatchInstanceStates(ctx context.Context) (<-chan worker.InstanceStateUpdate, <-chan error) {
	errCh := make(chan error, 1)
	outCh := make(chan worker.InstanceStateUpdate, 64)

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

func (w *FakeWorker) GetAllInstanceStates(ctx context.Context) ([]worker.InstanceStateUpdate, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var states []worker.InstanceStateUpdate
	for id, inst := range w.instances {
		var state worker.InstanceState
		switch inst.state {
		case "running":
			state = worker.StateRunning
		case "stopped", "exited":
			state = worker.StateExited
		default:
			state = worker.StateCreated
		}
		states = append(states, worker.InstanceStateUpdate{
			InstanceID:   id,
			InstanceName: inst.name,
			State:        state,
		})
	}
	return states, nil
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

func (w *FakeWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	if err := w.popFailure("EnsureDepot"); err != nil {
		return nil, err
	}
	depotDir := filepath.Join(w.tmpDir, "depot", fmt.Sprintf("%d", appID))
	if err := os.MkdirAll(depotDir, 0755); err != nil {
		return nil, err
	}
	return &worker.DepotResult{DepotDir: depotDir, Cached: true}, nil
}

func (w *FakeWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return worker.CopyDepotToVolume(depotDir, mountpoint)
}

func (w *FakeWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	return nil
}

func (w *FakeWorker) ListGameserverInstances(ctx context.Context) ([]worker.GameserverInstance, error) {
	return nil, nil
}

// Compile-time check that FakeWorker implements worker.Worker.
var _ worker.Worker = (*FakeWorker)(nil)
