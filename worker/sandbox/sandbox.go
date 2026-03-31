package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// SandboxWorker implements Worker using bwrap for isolation, systemd for lifecycle,
// and slirp4netns for network isolation. No Docker daemon required.
type SandboxWorker struct {
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string
	resolve   worker.VolumeResolver
	bwrapPath string
	slirpPath string

	mu        sync.Mutex
	instances map[string]*managedInstance

	eventMu     sync.Mutex
	eventCh     chan worker.InstanceEvent
	eventActive bool
}

type managedInstance struct {
	id        string
	name      string
	image     string
	pid       int
	startedAt time.Time
	exitCode  int
	exited    bool
	logFile   *os.File
	done      chan struct{}
	unitName  string // systemd unit name
	slirp     *slirpInstance
}

// instanceManifest is persisted to disk so StartInstance can reconstruct the config.
type instanceManifest struct {
	Name          string               `json:"name"`
	Image         string               `json:"image"`
	Env           []string             `json:"env"`
	Ports         []worker.PortBinding `json:"ports"`
	VolumeName    string               `json:"volume_name"`
	MemoryLimitMB int                  `json:"memory_limit_mb"`
	CPULimit      float64              `json:"cpu_limit"`
	Binds         []string             `json:"binds"`
}

func New(gameStore *games.GameStore, dataDir string, log *slog.Logger) *SandboxWorker {
	w := &SandboxWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		instances: make(map[string]*managedInstance),
		eventCh:   make(chan worker.InstanceEvent, 64),
	}
	w.resolve = w.volumeResolver()

	// Kill orphaned namespace holders from a previous crash
	cleanupOrphanHolders(log)

	// Ensure sandbox binaries are available
	bwrapPath, err := ensureBwrap(dataDir, log)
	if err != nil {
		log.Error("failed to ensure bwrap binary", "error", err)
	} else {
		w.bwrapPath = bwrapPath
	}

	slirpPath, err := ensureSlirp4netns(dataDir, log)
	if err != nil {
		log.Warn("slirp4netns not available — network isolation disabled", "error", err)
	} else {
		w.slirpPath = slirpPath
	}

	if !hasSystemdRun() {
		log.Warn("systemd-run not found — resource limits and process survival unavailable")
	} else {
		log.Info("sandbox runtime ready", "bwrap", w.bwrapPath, "slirp", w.slirpPath)
	}
	return w
}

func (w *SandboxWorker) volumeResolver() worker.VolumeResolver {
	return func(ctx context.Context, volumeName string) (string, error) {
		path := filepath.Join(w.dataDir, "volumes", volumeName)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("volume %s not found: %w", volumeName, err)
		}
		return path, nil
	}
}

func (w *SandboxWorker) imagesDir() string  { return filepath.Join(w.dataDir, "images") }
func (w *SandboxWorker) instanceDir(id string) string {
	return filepath.Join(w.dataDir, "instances", id)
}

// --- Worker interface: Instance lifecycle ---

func (w *SandboxWorker) PullImage(ctx context.Context, image string) error {
	_, err := pullAndExtractOCIImage(ctx, image, w.imagesDir(), w.log)
	return err
}

func (w *SandboxWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
	id := opts.Name

	dir := w.instanceDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating instance dir: %w", err)
	}

	manifest := instanceManifest{
		Name:          opts.Name,
		Image:         opts.Image,
		Env:           opts.Env,
		Ports:         opts.Ports,
		VolumeName:    opts.VolumeName,
		MemoryLimitMB: opts.MemoryLimitMB,
		CPULimit:      opts.CPULimit,
		Binds:         opts.Binds,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("marshaling instance manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644); err != nil {
		return "", fmt.Errorf("writing instance manifest: %w", err)
	}

	w.log.Info("instance created", "id", id, "image", opts.Image)
	return id, nil
}

func (w *SandboxWorker) StartInstance(ctx context.Context, id string) error {
	dir := w.instanceDir(id)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("reading instance manifest: %w", err)
	}

	var manifest instanceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parsing instance manifest: %w", err)
	}

	// Resolve extracted image rootfs
	rootFS, imgCfg, err := imageRootFS(w.imagesDir(), manifest.Image)
	if err != nil {
		return fmt.Errorf("resolving image rootfs: %w", err)
	}

	cmdArgs := append(imgCfg.Entrypoint, imgCfg.Cmd...)
	if len(cmdArgs) == 0 {
		return fmt.Errorf("image %s has no entrypoint or cmd", manifest.Image)
	}

	// Build bwrap command with full isolation
	bwrapArgs := buildBwrapArgs(rootFS, manifest, imgCfg, w.dataDir)
	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, cmdArgs...)

	// Set up network namespace before starting bwrap
	var slirpInst *slirpInstance
	if w.slirpPath != "" {
		si, err := setupNetworkNamespace(id, manifest.Ports, w.dataDir, w.slirpPath, w.log)
		if err != nil {
			w.log.Warn("network isolation unavailable", "id", id, "error", err)
		} else {
			slirpInst = si
		}
	}

	// Wrap in systemd-run for lifecycle + cgroups.
	// If we have a network namespace, wrap bwrap in nsenter to join it.
	cmd := buildSystemdCommandWithNetns(id, manifest, bwrapArgs, w.bwrapPath, slirpInst)

	// Log file
	logPath := filepath.Join(dir, "output.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		stopSlirp(slirpInst, w.log)
		return fmt.Errorf("creating log file: %w", err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		stopSlirp(slirpInst, w.log)
		return fmt.Errorf("starting instance: %w", err)
	}

	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}

	inst := &managedInstance{
		id:        id,
		name:      manifest.Name,
		image:     manifest.Image,
		pid:       pid,
		startedAt: time.Now(),
		logFile:   logFile,
		done:      make(chan struct{}),
		unitName:  "gj-" + id,
		slirp:     slirpInst,
	}

	w.mu.Lock()
	w.instances[id] = inst
	w.mu.Unlock()

	// Exit watcher
	go func() {
		cmd.Wait()
		inst.exitCode = cmd.ProcessState.ExitCode()
		inst.exited = true
		logFile.Close()
		stopSlirp(inst.slirp, w.log)

		w.log.Info("instance exited", "id", id, "exit_code", inst.exitCode)
		close(inst.done)

		w.eventMu.Lock()
		active := w.eventActive
		w.eventMu.Unlock()

		if active {
			select {
			case w.eventCh <- worker.InstanceEvent{
				InstanceID:   id,
				InstanceName: manifest.Name,
				Action:       "die",
			}:
			default:
			}
		}
	}()

	w.log.Info("instance started", "id", id, "pid", pid, "image", manifest.Image, "unit", inst.unitName)
	return nil
}

func (w *SandboxWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if !ok || inst.exited {
		return nil
	}

	// Send SIGTERM to the process group and systemd unit
	if inst.pid > 0 {
		syscall.Kill(-inst.pid, syscall.SIGTERM)
	}
	stopSystemdUnit(inst.unitName, w.log)

	select {
	case <-inst.done:
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("instance did not stop, killing", "id", id)
		if inst.pid > 0 {
			syscall.Kill(-inst.pid, syscall.SIGKILL)
		}
		killSystemdUnit(inst.unitName, w.log)
		<-inst.done
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *SandboxWorker) RemoveInstance(ctx context.Context, id string) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	if ok {
		delete(w.instances, id)
	}
	w.mu.Unlock()

	if ok && !inst.exited {
		killSystemdUnit(inst.unitName, w.log)
		<-inst.done
	} else if ok {
		stopSlirp(inst.slirp, w.log)
	}

	os.RemoveAll(w.instanceDir(id))
	return nil
}

func (w *SandboxWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("instance %s not found", id)
	}

	state := "running"
	if inst.exited {
		state = "exited"
	}

	return &worker.InstanceInfo{
		ID:        inst.id,
		State:     state,
		StartedAt: inst.startedAt,
		ExitCode:  inst.exitCode,
	}, nil
}

func (w *SandboxWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	w.mu.Lock()
	inst, ok := w.instances[instanceID]
	w.mu.Unlock()

	if !ok || inst.exited {
		return 1, "", "", fmt.Errorf("instance %s not found or not running", instanceID)
	}

	// Re-read manifest to rebuild bwrap for exec
	dir := w.instanceDir(instanceID)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return 1, "", "", err
	}
	var manifest instanceManifest
	json.Unmarshal(manifestData, &manifest)

	rootFS, imgCfg, err := imageRootFS(w.imagesDir(), manifest.Image)
	if err != nil {
		return 1, "", "", err
	}

	bwrapArgs := buildBwrapArgs(rootFS, manifest, imgCfg, w.dataDir)
	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, cmd...)

	execCmd := buildExecCommand(bwrapArgs, w.bwrapPath)
	var stdout, stderr []byte
	var stdoutBuf, stderrBuf safeBuffer
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = &stderrBuf

	err = execCmd.Run()
	stdout = stdoutBuf.Bytes()
	stderr = stderrBuf.Bytes()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 1, "", "", err
		}
	}

	return exitCode, string(stdout), string(stderr), nil
}

// --- Worker interface: Logs & Stats ---

func (w *SandboxWorker) InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error) {
	dir := w.instanceDir(instanceID)
	logPath := filepath.Join(dir, "output.log")

	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("opening instance log: %w", err)
	}

	if !follow {
		lines, err := tailFile(f, tail)
		if err != nil {
			f.Close()
			return nil, err
		}
		f.Close()

		var joined string
		for _, l := range lines {
			joined += l + "\n"
		}
		return io.NopCloser(io.Reader(ioStringReader(joined))), nil
	}

	// Seek to end for follow mode
	if tail == 0 {
		f.Seek(0, io.SeekEnd)
	}
	return newFollowReader(ctx, f, instanceID, w), nil
}

func (w *SandboxWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
	w.mu.Lock()
	inst, ok := w.instances[instanceID]
	w.mu.Unlock()

	if !ok || inst.exited {
		return &worker.InstanceStats{}, nil
	}

	return readCgroupStats(inst.unitName, inst.pid)
}

// --- Worker interface: Volumes ---

func (w *SandboxWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	// No chown needed — sandbox runs the game process as the same user as gamejanitor.
	// Docker runtime chowns to UID 1001 because the container runs as a different user.
	return nil
}

func (w *SandboxWorker) RemoveVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	err := os.RemoveAll(path)
	if err != nil {
		// Files created inside a user namespace may be owned by mapped UIDs
		// that the current user can't delete. Use unshare to enter a matching
		// user namespace where we have permission.
		cmd := exec.Command("unshare", "--user", "--map-root-user", "--", "rm", "-rf", path)
		if rmErr := cmd.Run(); rmErr != nil {
			return fmt.Errorf("removing volume %s: %w (fallback also failed: %v)", name, err, rmErr)
		}
	}
	return nil
}

func (w *SandboxWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	return worker.VolumeSizeDirect(w.resolve, ctx, volumeName)
}

// --- Worker interface: File operations (delegate to shared helpers) ---

func (w *SandboxWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	return worker.ListFilesDirect(w.resolve, ctx, volumeName, path)
}
func (w *SandboxWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	return worker.ReadFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *SandboxWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	return worker.OpenFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *SandboxWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	return worker.WriteFileDirect(w.resolve, ctx, volumeName, path, content, perm)
}
func (w *SandboxWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	return worker.WriteFileStreamDirect(w.resolve, ctx, volumeName, path, reader, perm)
}
func (w *SandboxWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	return worker.DeletePathDirect(w.resolve, ctx, volumeName, path)
}
func (w *SandboxWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	return worker.CreateDirectoryDirect(w.resolve, ctx, volumeName, path)
}
func (w *SandboxWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	return worker.RenamePathDirect(w.resolve, ctx, volumeName, from, to)
}
func (w *SandboxWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	return worker.DownloadFileDirect(w.resolve, ctx, volumeName, url, destPath, expectedHash, maxBytes)
}

// --- Worker interface: Copy operations ---

func (w *SandboxWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
	// In sandbox mode, instance filesystem is the volume — read directly
	dir := w.instanceDir(instanceID)
	manifestData, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest instanceManifest
	json.Unmarshal(manifestData, &manifest)
	if manifest.VolumeName == "" {
		return nil, fmt.Errorf("instance %s has no volume", instanceID)
	}
	mountpoint, err := w.resolve(ctx, manifest.VolumeName)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(mountpoint, path))
}

func (w *SandboxWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
	dir := w.instanceDir(instanceID)
	manifestData, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
	var manifest instanceManifest
	json.Unmarshal(manifestData, &manifest)
	if manifest.VolumeName == "" {
		return fmt.Errorf("instance %s has no volume", instanceID)
	}
	mountpoint, err := w.resolve(ctx, manifest.VolumeName)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(mountpoint, path)
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	return os.WriteFile(fullPath, content, 0644)
}

func (w *SandboxWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("CopyDirFromInstance not supported in sandbox mode")
}

func (w *SandboxWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	return fmt.Errorf("CopyTarToInstance not supported in sandbox mode")
}

// --- Worker interface: Backup/Restore ---

func (w *SandboxWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return worker.BackupVolumeDirect(w.resolve, ctx, volumeName)
}

func (w *SandboxWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	return worker.RestoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
}

// --- Worker interface: Events ---

func (w *SandboxWorker) WatchEvents(ctx context.Context) (<-chan worker.InstanceEvent, <-chan error) {
	w.eventMu.Lock()
	w.eventActive = true
	w.eventMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		w.eventMu.Lock()
		w.eventActive = false
		w.eventMu.Unlock()
	}()

	return w.eventCh, errCh
}

// --- Worker interface: Discovery ---

func (w *SandboxWorker) ListGameserverInstances(ctx context.Context) ([]worker.GameserverInstance, error) {
	// Scan instance directories for running processes
	instancesDir := filepath.Join(w.dataDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var result []worker.GameserverInstance
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		manifestPath := filepath.Join(instancesDir, id, "manifest.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			continue
		}
		var manifest instanceManifest
		if json.Unmarshal(data, &manifest) != nil {
			continue
		}

		w.mu.Lock()
		inst, running := w.instances[id]
		w.mu.Unlock()

		state := "exited"
		if running && !inst.exited {
			state = "running"
		}

		gsID, ok := naming.GameserverIDFromInstanceName(manifest.Name)
		if !ok {
			continue
		}

		result = append(result, worker.GameserverInstance{
			InstanceID:   id,
			InstanceName: manifest.Name,
			GameserverID: gsID,
			State:        state,
		})
	}

	return result, nil
}

// --- Worker interface: Game scripts & Steam ---

func (w *SandboxWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return worker.PrepareGameScripts(w.gameStore, w.dataDir, gameID, gameserverID)
}

func (w *SandboxWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	return worker.EnsureDepot(ctx, w.dataDir, w.log, appID, branch, accountName, refreshToken, onProgress)
}

func (w *SandboxWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return worker.CopyDepotToVolume(depotDir, mountpoint)
}

func (w *SandboxWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return worker.DownloadWorkshopItem(ctx, w.dataDir, w.log, appID, hcontentFile, filepath.Join(mountpoint, installPath))
}
