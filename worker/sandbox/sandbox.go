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
	"strconv"
	"strings"
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
	paths     *systemPaths

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
	cleanupOrphanHolders(log)

	paths, err := resolvePaths(dataDir, log)
	if err != nil {
		log.Error("sandbox runtime initialization failed", "error", err)
		paths = &systemPaths{IsRoot: os.Getuid() == 0}
	}

	w := &SandboxWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		paths:     paths,
		instances: make(map[string]*managedInstance),
		eventCh:   make(chan worker.InstanceEvent, 64),
	}
	w.resolve = w.volumeResolver()

	log.Info("sandbox runtime ready",
		"bwrap", paths.Bwrap,
		"slirp", paths.Slirp4netns,
		"systemd", paths.hasSystemd(),
		"network_isolation", paths.hasNetworkIsolation(),
		"root", paths.IsRoot)
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
	if w.paths.hasNetworkIsolation() {
		si, err := setupNetworkNamespace(id, manifest.Ports, w.dataDir, w.paths, w.log)
		if err != nil {
			w.log.Warn("network isolation failed for this instance — it will run on host network", "id", id, "error", err)
		} else {
			slirpInst = si
		}
	}

	// Wrap in systemd-run for lifecycle + cgroups.
	cmd := buildSystemdCommandWithNetns(id, manifest, bwrapArgs, w.paths, slirpInst)

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

		uptime := time.Since(inst.startedAt)
		if inst.exitCode != 0 && uptime < 3*time.Second {
			// Immediate exit with error — likely a sandbox/config problem, not a game crash.
			// Read the output log for the actual error.
			logData, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
			w.log.Error("instance failed to start (exited immediately)",
				"id", id, "exit_code", inst.exitCode, "uptime", uptime.Round(time.Millisecond),
				"output", truncate(string(logData), 500))
		} else {
			w.log.Info("instance exited", "id", id, "exit_code", inst.exitCode, "uptime", uptime.Round(time.Second))
		}
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

	// Send SIGTERM via cgroup + process group + systemd scope
	killCgroupProcesses(inst.unitName, syscall.SIGTERM, w.paths, w.log)
	if inst.pid > 0 {
		syscall.Kill(-inst.pid, syscall.SIGTERM) // process group
		syscall.Kill(inst.pid, syscall.SIGTERM)  // direct
	}
	stopSystemdUnit(inst.unitName, w.paths, w.log)

	select {
	case <-inst.done:
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("instance did not stop, killing", "id", id)
		killCgroupProcesses(inst.unitName, syscall.SIGKILL, w.paths, w.log)
		if inst.pid > 0 {
			syscall.Kill(-inst.pid, syscall.SIGKILL)
		}
		killSystemdUnit(inst.unitName, w.paths, w.log)
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
		killSystemdUnit(inst.unitName, w.paths, w.log)
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

	// Find the init PID inside the sandbox (bwrap's child) by reading the cgroup
	targetPID := findSandboxInitPID(inst.unitName, inst.pid, w.paths)
	if targetPID <= 0 {
		return 1, "", "", fmt.Errorf("could not find sandbox process for instance %s", instanceID)
	}

	// Enter the running instance's namespaces and execute the command
	nsArgs := []string{
		fmt.Sprintf("--target=%d", targetPID),
		"--mount", "--pid",
	}
	// Enter net namespace if we have network isolation
	if inst.slirp != nil {
		nsArgs = append(nsArgs, "--net")
	}
	if w.paths.IsRoot {
		// Root can enter all namespaces directly
	} else {
		nsArgs = append(nsArgs, "--user", "--preserve-credentials")
	}
	nsArgs = append(nsArgs, "--")
	nsArgs = append(nsArgs, cmd...)

	execCmd := exec.CommandContext(ctx, w.paths.Nsenter, nsArgs...)
	var stdoutBuf, stderrBuf safeBuffer
	execCmd.Stdout = &stdoutBuf
	execCmd.Stderr = &stderrBuf

	err := execCmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 1, "", "", fmt.Errorf("exec in instance %s: %w", instanceID, err)
		}
	}

	return exitCode, string(stdoutBuf.Bytes()), string(stderrBuf.Bytes()), nil
}

// findSandboxInitPID finds the PID of the init process inside the bwrap sandbox.
// This is the first child process in the cgroup (PID 1 inside the PID namespace).
func findSandboxInitPID(unitName string, parentPID int, paths *systemPaths) int {
	// Try cgroup first — most reliable
	if paths.hasSystemd() {
		args := []string{"show", "-p", "ControlGroup", unitName + ".scope"}
		if !paths.IsRoot {
			args = append([]string{"--user"}, args...)
		}
		out, err := exec.Command(paths.Systemctl, args...).Output()
		if err == nil {
			cgPath := strings.TrimPrefix(strings.TrimSpace(string(out)), "ControlGroup=")
			if cgPath != "" {
				procsPath := "/sys/fs/cgroup" + cgPath + "/cgroup.procs"
				data, err := os.ReadFile(procsPath)
				if err == nil {
					// Return the lowest PID in the cgroup — that's the init process
					lowestPID := 0
					for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
						pid, err := strconv.Atoi(strings.TrimSpace(line))
						if err == nil && pid > 0 && (lowestPID == 0 || pid < lowestPID) {
							lowestPID = pid
						}
					}
					if lowestPID > 0 {
						return lowestPID
					}
				}
			}
		}
	}

	// Fallback: walk /proc to find a child of our parent PID
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		// stat format: pid (comm) state ppid ...
		fields := strings.Fields(string(stat))
		if len(fields) > 3 {
			ppid, _ := strconv.Atoi(fields[3])
			if ppid == parentPID {
				return pid
			}
		}
	}
	return 0
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

	// For follow mode, start from the beginning so we catch startup logs.
	// Docker streams from the start too — the ReadyWatcher needs to see
	// the full output to detect the ready pattern.
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
