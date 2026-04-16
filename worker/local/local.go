package local

import (
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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/util/naming"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// LocalWorker implements Worker using crun for OCI container execution and
// systemd for lifecycle management. Uses host networking. No external daemon required.
type LocalWorker struct {
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string
	resolve   VolumeResolver
	paths     *systemPaths
	rt        *runtime.Runtime

	mu        sync.Mutex
	instances map[string]*managedInstance

	pullMu sync.Mutex // serializes image pulls to prevent index corruption

	tracker *InstanceTracker
}

type managedInstance struct {
	id        string
	name      string
	image     string
	pid       int
	startedAt time.Time
	exitCode  atomic.Int32
	exited    atomic.Bool
	logWriter *rotatingWriter
	done     chan struct{}
	unitName string // systemd unit name
}

// instanceState is persisted alongside the manifest so running instances
// can be re-adopted after a gamejanitor restart.
type instanceState struct {
	StartedAt time.Time `json:"started_at"`
	HolderPID int       `json:"holder_pid,omitempty"`
	UnitName  string    `json:"unit_name"`
}

func saveInstanceState(dir string, state instanceState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshaling instance state: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), data, 0644)
}

func loadInstanceState(dir string) (*instanceState, error) {
	data, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, err
	}
	var state instanceState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("parsing instance state: %w", err)
	}
	return &state, nil
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
	Entrypoint    []string             `json:"entrypoint,omitempty"`
}

func New(gameStore *games.GameStore, dataDir string, log *slog.Logger) *LocalWorker {
	cleanupOrphanHolders(log)
	cleanupOverlayMounts(dataDir, log)

	paths, err := resolvePaths(dataDir, log)
	if err != nil {
		log.Error("runtime initialization failed", "error", err)
		paths = &systemPaths{IsRoot: os.Getuid() == 0}
	}

	rt, err := runtime.New(dataDir, log)
	if err != nil {
		log.Error("crun runtime initialization failed", "error", err)
	}

	w := &LocalWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		paths:     paths,
		rt:        rt,
		instances: make(map[string]*managedInstance),
	}
	w.tracker = NewInstanceTracker(log)
	w.resolve = w.volumeResolver()
	w.recoverInstances()

	log.Info("runtime ready",
		"crun", rt != nil,
		"systemd", paths.hasSystemd(),
		"root", paths.IsRoot)
	return w
}

func (w *LocalWorker) volumeResolver() VolumeResolver {
	return func(ctx context.Context, volumeName string) (string, error) {
		path := filepath.Join(w.dataDir, "volumes", volumeName)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("volume %s not found: %w", volumeName, err)
		}
		return path, nil
	}
}

func (w *LocalWorker) imagesDir() string  { return filepath.Join(w.dataDir, "images") }
func (w *LocalWorker) instanceDir(id string) string {
	return filepath.Join(w.dataDir, "instances", id)
}

// --- Worker interface: Instance lifecycle ---

func (w *LocalWorker) PullImage(ctx context.Context, image string, onProgress func(worker.PullProgress)) error {
	w.pullMu.Lock()
	defer w.pullMu.Unlock()
	_, err := pullAndExtractOCIImage(ctx, image, w.imagesDir(), onProgress, w.log)
	return err
}

func (w *LocalWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
	if opts.Name == "" {
		return "", fmt.Errorf("instance name is required")
	}
	if opts.Image == "" {
		return "", fmt.Errorf("instance image is required")
	}

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
		Entrypoint:    opts.Entrypoint,
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

func (w *LocalWorker) StartInstance(ctx context.Context, id string, readyPattern string) error {
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

	// Use manifest entrypoint override if set, otherwise fall back to image config
	var cmdArgs []string
	if len(manifest.Entrypoint) > 0 {
		cmdArgs = manifest.Entrypoint
	} else {
		cmdArgs = append(imgCfg.Entrypoint, imgCfg.Cmd...)
	}
	if len(cmdArgs) == 0 {
		return fmt.Errorf("image %s has no entrypoint or cmd", manifest.Image)
	}

	uid, gid := parseImageUser(imgCfg.User, rootFS)

	// Build environment: image config env + manifest env (user overrides)
	// HOME must always be set — many programs (Steam SDK, .NET, etc.) expect it
	env := []string{"HOME=/home/gameserver"}
	env = append(env, imgCfg.Env...)
	env = append(env, manifest.Env...)

	// Build bind mounts
	var mounts []runtime.Mount

	// DNS config
	resolvConf := filepath.Join(w.dataDir, "resolv.conf")
	if _, err := os.Stat(resolvConf); err == nil {
		mounts = append(mounts, runtime.Mount{Source: resolvConf, Destination: "/etc/resolv.conf", Options: []string{"rbind", "ro"}})
	} else if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		mounts = append(mounts, runtime.Mount{Source: "/etc/resolv.conf", Destination: "/etc/resolv.conf", Options: []string{"rbind", "ro"}})
	}

	// Host SSL certs if the rootfs doesn't have its own
	rootFSCerts := filepath.Join(rootFS, "etc/ssl/certs/ca-certificates.crt")
	if _, err := os.Stat(rootFSCerts); err != nil {
		for _, certPath := range []struct{ src, dst string }{
			{"/etc/ssl/certs", "/etc/ssl/certs"},
			{"/etc/pki/tls/certs", "/etc/pki/tls/certs"},
		} {
			if _, err := os.Stat(certPath.src); err == nil {
				mounts = append(mounts, runtime.Mount{Source: certPath.src, Destination: certPath.dst, Options: []string{"rbind", "ro"}})
			}
		}
	}

	// Volume mount
	if manifest.VolumeName != "" {
		volumeDir := filepath.Join(w.dataDir, "volumes", manifest.VolumeName)
		mounts = append(mounts, runtime.Mount{Source: volumeDir, Destination: "/data", Options: []string{"rbind", "rw"}})
	}

	// Bind-mount host paths (scripts, defaults, depot)
	for _, bind := range manifest.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) >= 2 {
			opts := []string{"rbind", "rw"}
			if len(parts) == 3 && strings.Contains(parts[2], "ro") {
				opts = []string{"rbind", "ro"}
			}
			mounts = append(mounts, runtime.Mount{Source: parts[0], Destination: parts[1], Options: opts})
		}
	}

	workDir := imgCfg.WorkingDir
	if workDir == "" {
		workDir = "/data"
	}

	bundleDir := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundleDir, 0755); err != nil {
		return fmt.Errorf("creating bundle dir: %w", err)
	}

	if err := runtime.PrepareBundle(bundleDir, runtime.BundleConfig{
		RootFS:   rootFS,
		Env:      env,
		Cmd:      cmdArgs,
		WorkDir:  workDir,
		Hostname: manifest.Name,
		Binds:    mounts,
		UID:      uid,
		GID:      gid,
		MemoryMB: manifest.MemoryLimitMB,
		CPUQuota: manifest.CPULimit,
	}); err != nil {
		return fmt.Errorf("preparing OCI bundle: %w", err)
	}

	// Wrap in systemd-run for lifecycle management
	cmd := buildSystemdCrunCommand(id, bundleDir, w.rt, w.paths)

	// Log file with size-based rotation (50MB cap, 1 backup)
	logPath := filepath.Join(dir, "output.log")
	logWriter, err := newRotatingWriter(logPath)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logWriter.Close()
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
		logWriter: logWriter,
		done:     make(chan struct{}),
		unitName: "gj-" + id,
	}

	w.mu.Lock()
	w.instances[id] = inst
	w.mu.Unlock()

	state := instanceState{
		StartedAt: inst.startedAt,
		UnitName:  inst.unitName,
	}
	if err := saveInstanceState(dir, state); err != nil {
		w.log.Warn("failed to persist instance state", "id", id, "error", err)
	}

	if w.tracker != nil {
		w.tracker.Track(id, manifest.Name)
		// Process is alive once the systemd scope is up — mark Running now. Ready
		// is a separate signal set by WatchLogs (or immediately below if no pattern).
		w.tracker.SetState(id, worker.StateRunning)

		logReader, err := w.InstanceLogs(context.Background(), id, 0, true)
		if err == nil {
			w.tracker.WatchLogs(context.Background(), id, readyPattern, logReader)
		}
	}

	// Exit watcher
	go func() {
		cmd.Wait()
		inst.exitCode.Store(int32(cmd.ProcessState.ExitCode()))
		inst.exited.Store(true)
		if inst.logWriter != nil {
			inst.logWriter.Close()
		}
		w.rt.Delete(id, true)
		stopSystemdUnit(inst.unitName, w.paths, w.log)

		uptime := time.Since(inst.startedAt)
		exitCode := inst.exitCode.Load()
		// Extract the signal that killed the process (if any)
		var signalInfo string
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			signalInfo = status.Signal().String()
		}

		if exitCode != 0 && uptime < 3*time.Second {
			logData, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
			w.log.Error("instance failed to start (exited immediately)",
				"id", id, "exit_code", exitCode, "signal", signalInfo, "uptime", uptime.Round(time.Millisecond),
				"output", truncate(string(logData), 500))
		} else {
			w.log.Info("instance exited", "id", id, "exit_code", exitCode, "signal", signalInfo, "uptime", uptime.Round(time.Second))
		}
		os.Remove(filepath.Join(w.instanceDir(id), "state.json"))
		close(inst.done)

		if w.tracker != nil {
			w.tracker.SetExited(id, int(inst.exitCode.Load()))
		}
	}()

	w.log.Info("instance started", "id", id, "pid", pid, "image", manifest.Image, "unit", inst.unitName)
	return nil
}

func (w *LocalWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
		return nil
	}

	w.log.Info("StopInstance called", "id", id, "pid", inst.pid, "uptime", time.Since(inst.startedAt).Round(time.Second))

	// Send SIGTERM via crun kill
	if err := w.rt.Kill(id, "SIGTERM"); err != nil {
		w.log.Debug("crun kill SIGTERM failed, trying systemd fallback", "id", id, "error", err)
		// Fallback to systemctl kill
		if w.paths.hasSystemd() {
			prefix := systemctlPrefix(w.paths)
			exec.Command(w.paths.Systemctl, append(prefix, "kill", "--signal=TERM", inst.unitName+".scope")...).Run()
		}
	}

	select {
	case <-inst.done:
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("instance did not stop, killing", "id", id)
		// Force kill via crun
		w.rt.Kill(id, "SIGKILL")
		w.rt.Delete(id, true)
		// Fallback: kill systemd scope
		killSystemdUnit(inst.unitName, w.paths, w.log)
		<-inst.done
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *LocalWorker) RemoveInstance(ctx context.Context, id string) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	if ok {
		delete(w.instances, id)
	}
	w.mu.Unlock()

	if ok && !inst.exited.Load() {
		w.log.Warn("RemoveInstance: killing running instance", "id", id, "pid", inst.pid)
		w.rt.Kill(id, "SIGKILL")
		w.rt.Delete(id, true)
		killSystemdUnit(inst.unitName, w.paths, w.log)
		<-inst.done
	}

	if w.tracker != nil {
		w.tracker.Remove(id)
	}

	os.RemoveAll(w.instanceDir(id))
	return nil
}

func (w *LocalWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if ok {
		state := "running"
		if inst.exited.Load() {
			state = "exited"
		}
		return &worker.InstanceInfo{
			ID:        inst.id,
			State:     state,
			StartedAt: inst.startedAt,
			ExitCode:  int(inst.exitCode.Load()),
		}, nil
	}

	// Not in memory — check persisted state for instances surviving a restart
	dir := w.instanceDir(id)
	state, err := loadInstanceState(dir)
	if err != nil {
		return nil, fmt.Errorf("instance %s not found", id)
	}

	unitName := state.UnitName
	if isSystemdScopeActive(unitName, w.paths) {
		return &worker.InstanceInfo{
			ID:        id,
			State:     "running",
			StartedAt: state.StartedAt,
		}, nil
	}

	return &worker.InstanceInfo{
		ID:    id,
		State: "exited",
	}, nil
}

func (w *LocalWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	w.mu.Lock()
	inst, ok := w.instances[instanceID]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
		return 1, "", "", fmt.Errorf("instance %s not found or not running", instanceID)
	}

	// Build environment from manifest: image config env + manifest env
	dir := w.instanceDir(instanceID)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return 1, "", "", fmt.Errorf("reading manifest for exec: %w", err)
	}
	var manifest instanceManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return 1, "", "", fmt.Errorf("parsing manifest for exec: %w", err)
	}

	_, imgCfg, err := imageRootFS(w.imagesDir(), manifest.Image)
	if err != nil {
		return 1, "", "", fmt.Errorf("resolving image config for exec: %w", err)
	}

	env := []string{"HOME=/home/gameserver"}
	env = append(env, imgCfg.Env...)
	env = append(env, manifest.Env...)

	return w.rt.Exec(instanceID, cmd, env)
}

// cgroupPIDs returns all PIDs in the systemd scope for the given unit.
func cgroupPIDs(unitName string, paths *systemPaths) []int {
	if !paths.hasSystemd() {
		return nil
	}
	args := []string{"show", "-p", "ControlGroup", unitName + ".scope"}
	if !paths.IsRoot {
		args = append([]string{"--user"}, args...)
	}
	out, err := exec.Command(paths.Systemctl, args...).Output()
	if err != nil {
		return nil
	}
	cgPath := strings.TrimPrefix(strings.TrimSpace(string(out)), "ControlGroup=")
	if cgPath == "" {
		return nil
	}
	data, err := os.ReadFile("/sys/fs/cgroup" + cgPath + "/cgroup.procs")
	if err != nil {
		return nil
	}
	var pids []int
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		pid, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// --- Worker interface: Logs & Stats ---

func (w *LocalWorker) InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error) {
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
		return io.NopCloser(strings.NewReader(joined)), nil
	}

	// For follow mode, start from the beginning so we catch startup logs.
	// Streams from the start — the worker-side ready watcher needs to see
	// the full output to detect the ready pattern.
	return newFollowReader(ctx, f, instanceID, w), nil
}

func (w *LocalWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
	w.mu.Lock()
	inst, ok := w.instances[instanceID]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
		return &worker.InstanceStats{}, nil
	}

	stats, err := readCgroupStats(inst.unitName, inst.pid)
	if err != nil {
		return stats, err
	}

	// If cgroup didn't provide a memory limit, read from the manifest
	if stats.MemoryLimitMB == 0 {
		dir := w.instanceDir(instanceID)
		manifestData, _ := os.ReadFile(filepath.Join(dir, "manifest.json"))
		var manifest instanceManifest
		if json.Unmarshal(manifestData, &manifest) == nil && manifest.MemoryLimitMB > 0 {
			stats.MemoryLimitMB = manifest.MemoryLimitMB
		}
	}

	return stats, nil
}

// --- Worker interface: Volumes ---

func (w *LocalWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	// No chown. crun handles UID mapping via the OCI spec.
	return nil
}

func (w *LocalWorker) RemoveVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	err := os.RemoveAll(path)
	if err != nil {
		// Files created inside a user namespace are owned by subordinate UIDs
		// that the current user can't delete. Create a temporary user namespace
		// with the same UID mapping and delete from inside it.
		rmErr := removeWithUserNS(path, w.paths, w.log)
		if rmErr != nil {
			return fmt.Errorf("removing volume %s: %w (ns fallback: %v)", name, err, rmErr)
		}
	}
	return nil
}

// removeWithUserNS removes a path that may contain files owned by mapped UIDs.
func removeWithUserNS(path string, paths *systemPaths, log *slog.Logger) error {
	if paths.IsRoot {
		return exec.Command(paths.Rm, "-rf", path).Run()
	}

	holder := exec.Command(paths.Unshare, "--user", "--fork", "--kill-child", "--", paths.Sleep, "10")
	holder.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := holder.Start(); err != nil {
		return fmt.Errorf("starting cleanup namespace: %w", err)
	}
	defer func() { holder.Process.Kill(); holder.Wait() }()

	pid := holder.Process.Pid
	if paths.hasUIDMapping() {
		uid := os.Getuid()
		gid := os.Getgid()
		uidStart, uidCount := SubUIDRange()
		gidStart, gidCount := SubGIDRange()
		exec.Command(paths.NewUIDMap, fmt.Sprintf("%d", pid),
			"0", fmt.Sprintf("%d", uid), "1",
			"1", fmt.Sprintf("%d", uidStart), fmt.Sprintf("%d", uidCount)).Run()
		exec.Command(paths.NewGIDMap, fmt.Sprintf("%d", pid),
			"0", fmt.Sprintf("%d", gid), "1",
			"1", fmt.Sprintf("%d", gidStart), fmt.Sprintf("%d", gidCount)).Run()
	}

	return exec.Command(paths.Nsenter, fmt.Sprintf("--user=/proc/%d/ns/user", pid),
		"--", paths.Rm, "-rf", path).Run()
}

func (w *LocalWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	return VolumeSizeDirect(w.resolve, ctx, volumeName)
}

// --- Worker interface: File operations (delegate to shared helpers) ---

func (w *LocalWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	return ListFilesDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	return ReadFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	return OpenFileDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	return WriteFileDirect(w.resolve, ctx, volumeName, path, content, perm)
}
func (w *LocalWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	return WriteFileStreamDirect(w.resolve, ctx, volumeName, path, reader, perm)
}
func (w *LocalWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	return DeletePathDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	return CreateDirectoryDirect(w.resolve, ctx, volumeName, path)
}
func (w *LocalWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	return RenamePathDirect(w.resolve, ctx, volumeName, from, to)
}
func (w *LocalWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	return DownloadFileDirect(w.resolve, ctx, volumeName, url, destPath, expectedHash, maxBytes)
}

// --- Worker interface: Copy operations ---

func (w *LocalWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
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

func (w *LocalWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
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

func (w *LocalWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("CopyDirFromInstance not supported in sandbox mode")
}

func (w *LocalWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	return fmt.Errorf("CopyTarToInstance not supported in sandbox mode")
}

// --- Worker interface: Backup/Restore ---

func (w *LocalWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return BackupVolumeDirect(w.resolve, ctx, volumeName)
}

func (w *LocalWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	return RestoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
}

// --- Worker interface: Events ---

func (w *LocalWorker) WatchInstanceStates(ctx context.Context) (<-chan worker.InstanceStateUpdate, <-chan error) {
	errCh := make(chan error, 1)
	return w.tracker.Events(), errCh
}

func (w *LocalWorker) GetAllInstanceStates(ctx context.Context) ([]worker.InstanceStateUpdate, error) {
	return w.tracker.Snapshot(), nil
}

// --- Worker interface: Discovery ---

func (w *LocalWorker) ListGameserverInstances(ctx context.Context) ([]worker.GameserverInstance, error) {
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
		inst, inMemory := w.instances[id]
		w.mu.Unlock()

		state := "exited"
		if inMemory && !inst.exited.Load() {
			state = "running"
		} else if !inMemory {
			// Check persisted state for instances not yet in memory
			if persisted, err := loadInstanceState(filepath.Join(instancesDir, id)); err == nil {
				if isSystemdScopeActive(persisted.UnitName, w.paths) {
					state = "running"
				}
			}
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

// --- Recovery ---

// recoverInstances scans for active gj-*.scope systemd units and re-adopts
// instances that survived a gamejanitor restart.
func (w *LocalWorker) recoverInstances() {
	if !w.paths.hasSystemd() {
		return
	}

	prefix := systemctlPrefix(w.paths)
	args := append(prefix, "list-units", "--type=scope", "--state=active", "--no-legend", "--plain")
	out, err := exec.Command(w.paths.Systemctl, args...).Output()
	if err != nil {
		w.log.Debug("could not list active scopes for recovery", "error", err)
		return
	}

	var recovered int
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		scope := fields[0]
		if !strings.HasPrefix(scope, "gj-") || !strings.HasSuffix(scope, ".scope") {
			continue
		}
		unitName := strings.TrimSuffix(scope, ".scope")
		id := strings.TrimPrefix(unitName, "gj-")

		dir := w.instanceDir(id)
		manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			w.log.Warn("active scope has no manifest, stopping", "unit", unitName)
			stopSystemdUnit(unitName, w.paths, w.log)
			continue
		}
		var manifest instanceManifest
		if json.Unmarshal(manifestData, &manifest) != nil {
			w.log.Warn("active scope has corrupt manifest, stopping", "unit", unitName)
			stopSystemdUnit(unitName, w.paths, w.log)
			continue
		}

		state, err := loadInstanceState(dir)
		if err != nil {
			w.log.Warn("active scope has no state file (pre-recovery version), stopping", "unit", unitName)
			stopSystemdUnit(unitName, w.paths, w.log)
			continue
		}

		// Verify the scope's cgroup still has live processes
		pids := cgroupPIDs(unitName, w.paths)
		if len(pids) == 0 {
			w.log.Warn("active scope has no processes, skipping recovery", "unit", unitName)
			os.Remove(filepath.Join(dir, "state.json"))
			continue
		}

		inst := &managedInstance{
			id:        id,
			name:      manifest.Name,
			image:     manifest.Image,
			startedAt: state.StartedAt,
			done:      make(chan struct{}),
			unitName:  unitName,
		}

		w.mu.Lock()
		w.instances[id] = inst
		w.mu.Unlock()

		if w.tracker != nil {
			w.tracker.Recover(id, manifest.Name, worker.StateRunning, state.StartedAt, true)
		}

		// Polling exit watcher — checks scope liveness every 2 seconds
		go func(inst *managedInstance, dir string) {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if !isSystemdScopeActive(inst.unitName, w.paths) {
					inst.exited.Store(true)
					inst.exitCode.Store(-1)
					w.rt.Delete(inst.id, true)
					os.Remove(filepath.Join(dir, "state.json"))

					w.log.Info("recovered instance exited", "id", inst.id,
						"uptime", time.Since(inst.startedAt).Round(time.Second))
					close(inst.done)

					if w.tracker != nil {
						w.tracker.SetExited(inst.id, int(inst.exitCode.Load()))
					}
					return
				}
			}
		}(inst, dir)

		recovered++
		w.log.Info("recovered running instance", "id", id, "unit", unitName,
			"started_at", state.StartedAt)
	}

	if recovered > 0 {
		w.log.Info("instance recovery complete", "recovered", recovered)
	}
}

// --- Worker interface: Game scripts & Steam ---

func (w *LocalWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return PrepareGameScripts(w.gameStore, w.dataDir, gameID, gameserverID)
}

func (w *LocalWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	return EnsureDepot(ctx, w.dataDir, w.log, appID, branch, accountName, refreshToken, onProgress)
}

func (w *LocalWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return CopyDepotToVolume(depotDir, mountpoint)
}

func (w *LocalWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return DownloadWorkshopItem(ctx, w.dataDir, w.log, appID, hcontentFile, filepath.Join(mountpoint, installPath))
}
