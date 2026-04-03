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
	"sync/atomic"
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

	pullMu sync.Mutex // serializes image pulls to prevent index corruption

	tracker *worker.InstanceTracker
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
	done      chan struct{}
	unitName  string // systemd unit name
	slirp     *slirpInstance
}

// instanceState is persisted alongside the manifest so running instances
// can be re-adopted after a gamejanitor restart.
type instanceState struct {
	StartedAt   time.Time `json:"started_at"`
	HolderPID   int       `json:"holder_pid,omitempty"`
	SlirpPID    int       `json:"slirp_pid,omitempty"`
	NsPID       int       `json:"ns_pid,omitempty"`
	SlirpSocket string    `json:"slirp_socket,omitempty"`
	UnitName    string    `json:"unit_name"`
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

func New(gameStore *games.GameStore, dataDir string, log *slog.Logger) *SandboxWorker {
	cleanupOrphanHolders(log)
	cleanupOverlayMounts(dataDir, log)

	paths, err := resolvePaths(dataDir, log)
	if err != nil {
		log.Error("sandbox runtime initialization failed", "error", err)
		paths = &systemPaths{IsRoot: os.Getuid() == 0}
	}

	// Sandbox uses --unshare-user which maps the caller's UID to the game
	// user inside the namespace. Files must stay owned by the caller on the
	// host — chowning to UID 1001 would make them inaccessible inside.

	w := &SandboxWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		paths:     paths,
		instances: make(map[string]*managedInstance),
	}
	w.tracker = worker.NewInstanceTracker(log)
	w.resolve = w.volumeResolver()
	w.recoverInstances()

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
	w.pullMu.Lock()
	defer w.pullMu.Unlock()
	_, err := pullAndExtractOCIImage(ctx, image, w.imagesDir(), w.log)
	return err
}

func (w *SandboxWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
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

func (w *SandboxWorker) StartInstance(ctx context.Context, id string, readyPattern string) error {
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

	// Log file with size-based rotation (50MB cap, 1 backup)
	logPath := filepath.Join(dir, "output.log")
	logWriter, err := newRotatingWriter(logPath)
	if err != nil {
		stopSlirp(slirpInst, w.log)
		return fmt.Errorf("creating log file: %w", err)
	}

	cmd.Stdout = logWriter
	cmd.Stderr = logWriter
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		logWriter.Close()
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
		logWriter: logWriter,
		done:      make(chan struct{}),
		unitName:  "gj-" + id,
		slirp:     slirpInst,
	}

	w.mu.Lock()
	w.instances[id] = inst
	w.mu.Unlock()

	state := instanceState{
		StartedAt: inst.startedAt,
		UnitName:  inst.unitName,
	}
	if inst.slirp != nil {
		state.HolderPID = inst.slirp.holderPID
		state.SlirpPID = inst.slirp.slirpPID
		state.NsPID = inst.slirp.nsPID
		state.SlirpSocket = inst.slirp.apiSock
	}
	if err := saveInstanceState(dir, state); err != nil {
		w.log.Warn("failed to persist instance state", "id", id, "error", err)
	}

	if w.tracker != nil {
		w.tracker.Track(id, manifest.Name)
		w.tracker.SetState(id, worker.StateStarting)

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
		stopSlirp(inst.slirp, w.log)
		stopSystemdUnit(inst.unitName, w.paths, w.log)

		uptime := time.Since(inst.startedAt)
		if inst.exitCode.Load() != 0 && uptime < 3*time.Second {
			// Immediate exit with error — likely a sandbox/config problem, not a game crash.
			// Read the output log for the actual error.
			logData, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
			w.log.Error("instance failed to start (exited immediately)",
				"id", id, "exit_code", inst.exitCode.Load(), "uptime", uptime.Round(time.Millisecond),
				"output", truncate(string(logData), 500))
		} else {
			w.log.Info("instance exited", "id", id, "exit_code", inst.exitCode.Load(), "uptime", uptime.Round(time.Second))
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

func (w *SandboxWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
		return nil
	}

	// Send SIGTERM via systemctl kill — most reliable, reaches all processes in the scope
	if w.paths.hasSystemd() {
		prefix := systemctlPrefix(w.paths)
		exec.Command(w.paths.Systemctl, append(prefix, "kill", "--signal=TERM", inst.unitName+".scope")...).Run()
	}
	// Also send via process group and cgroup as fallback
	killCgroupProcesses(inst.unitName, syscall.SIGTERM, w.paths, w.log)
	if inst.pid > 0 {
		syscall.Kill(-inst.pid, syscall.SIGTERM)
		syscall.Kill(inst.pid, syscall.SIGTERM)
	}

	select {
	case <-inst.done:
		// Process exited gracefully — slirp cleaned up by exit watcher
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("instance did not stop, killing", "id", id)
		// Force kill everything in the scope
		if w.paths.hasSystemd() {
			prefix := systemctlPrefix(w.paths)
			exec.Command(w.paths.Systemctl, append(prefix, "kill", "--signal=KILL", inst.unitName+".scope")...).Run()
		}
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

	if ok && !inst.exited.Load() {
		killSystemdUnit(inst.unitName, w.paths, w.log)
		<-inst.done
	} else if ok {
		stopSlirp(inst.slirp, w.log)
	}

	if w.tracker != nil {
		w.tracker.Remove(id)
	}

	os.RemoveAll(w.instanceDir(id))
	return nil
}

func (w *SandboxWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
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

func (w *SandboxWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	w.mu.Lock()
	inst, ok := w.instances[instanceID]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
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

	// Inherit the sandbox's environment (PATH, RCON_PORT, etc.) so scripts
	// can find binaries and config inside the sandbox. The nsenter target is
	// the inner bwrap which doesn't have the bwrap-configured env vars — those
	// are set on its children (the actual entrypoint). Find a child to read from.
	envPID := findChildPID(targetPID)
	if envPID == 0 {
		envPID = targetPID
	}
	nsArgs = append(nsArgs, "--")
	nsArgs = append(nsArgs, "/usr/bin/env", "-i")
	nsArgs = append(nsArgs, sandboxEnv(envPID)...)
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

// findChildPID returns the first child PID of the given process, or 0 if none found.
func findChildPID(parentPID int) int {
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

// sandboxEnv reads the environment of a running process from /proc/<pid>/environ
// and returns it as a slice of "KEY=VALUE" strings suitable for passing to `env`.
func sandboxEnv(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil
	}
	var env []string
	for _, entry := range strings.Split(string(data), "\x00") {
		if entry != "" {
			env = append(env, entry)
		}
	}
	return env
}

// findSandboxInitPID finds the PID of the init process inside the bwrap sandbox.
// bwrap forks: the outer process stays in the host namespaces while the inner
// process (and its children) live inside the sandbox's mount/PID namespaces.
// We need a PID that's actually inside the sandbox so nsenter can access the
// bind-mounted /scripts directory. We detect this via /proc/<pid>/status NSpid:
// processes inside a nested PID namespace have multiple NSpid entries.
func findSandboxInitPID(unitName string, parentPID int, paths *systemPaths) int {
	pids := cgroupPIDs(unitName, paths)
	if len(pids) == 0 {
		pids = childPIDs(parentPID)
	}

	// Find the lowest PID that's inside a nested PID namespace (NSpid has 2+ entries).
	// This is the sandbox init process (PID 1 inside the namespace).
	lowestNamespacedPID := 0
	for _, pid := range pids {
		if !isInNestedPIDNamespace(pid) {
			continue
		}
		if lowestNamespacedPID == 0 || pid < lowestNamespacedPID {
			lowestNamespacedPID = pid
		}
	}
	return lowestNamespacedPID
}

// isInNestedPIDNamespace checks if a process lives inside a nested PID namespace
// by reading its NSpid line from /proc/<pid>/status. Processes in a child PID
// namespace have multiple NSpid values (e.g. "NSpid: 12345  1").
func isInNestedPIDNamespace(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "NSpid:") {
			fields := strings.Fields(line)
			// "NSpid: <host_pid> <ns_pid>" = 3+ fields means nested namespace
			return len(fields) >= 3
		}
	}
	return false
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

// childPIDs returns PIDs whose parent is the given PID (fallback when cgroup lookup fails).
func childPIDs(parentPID int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
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
				pids = append(pids, pid)
			}
		}
	}
	return pids
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
		return io.NopCloser(strings.NewReader(joined)), nil
	}

	// For follow mode, start from the beginning so we catch startup logs.
	// Docker streams from the start too — the worker-side ready watcher needs to see
	// the full output to detect the ready pattern.
	return newFollowReader(ctx, f, instanceID, w), nil
}

func (w *SandboxWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
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

	// Network I/O from the namespace's tap0 interface
	if inst.slirp != nil && inst.slirp.nsPID > 0 {
		rx, tx := readNetDevBytes(inst.slirp.nsPID, "tap0")
		stats.NetRxBytes = rx
		stats.NetTxBytes = tx
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

func (w *SandboxWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("creating volume: %w", err)
	}
	// No chown. bwrap --unshare-user maps the caller's UID to appear as the
	// image's UID inside the namespace. Root-owned files on the host are
	// accessible as the game user inside the sandbox.
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

func (w *SandboxWorker) WatchInstanceStates(ctx context.Context) (<-chan worker.InstanceStateUpdate, <-chan error) {
	errCh := make(chan error, 1)
	return w.tracker.Events(), errCh
}

func (w *SandboxWorker) GetAllInstanceStates(ctx context.Context) ([]worker.InstanceStateUpdate, error) {
	return w.tracker.Snapshot(), nil
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
func (w *SandboxWorker) recoverInstances() {
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

		// Rebuild slirp instance from persisted PIDs if network isolation was active
		var slirpInst *slirpInstance
		if state.HolderPID > 0 && state.SlirpPID > 0 {
			holderAlive := isPIDAlive(state.HolderPID, "unshare", "sleep")
			slirpAlive := isPIDAlive(state.SlirpPID, "slirp4netns")
			if holderAlive && slirpAlive {
				slirpInst = &slirpInstance{
					holderPID: state.HolderPID,
					slirpPID:  state.SlirpPID,
					nsPID:     state.NsPID,
					apiSock:   state.SlirpSocket,
				}
			} else {
				w.log.Warn("network isolation processes dead, instance recovered without network isolation",
					"id", id, "holder_alive", holderAlive, "slirp_alive", slirpAlive)
			}
		}

		inst := &managedInstance{
			id:        id,
			name:      manifest.Name,
			image:     manifest.Image,
			startedAt: state.StartedAt,
			done:      make(chan struct{}),
			unitName:  unitName,
			slirp:     slirpInst,
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
					stopSlirp(inst.slirp, w.log)
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
			"started_at", state.StartedAt, "network_isolation", slirpInst != nil)
	}

	if recovered > 0 {
		w.log.Info("instance recovery complete", "recovered", recovered)
	}
}

// isPIDAlive checks that a PID exists and its cmdline contains one of the expected substrings.
func isPIDAlive(pid int, expectedNames ...string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	for _, name := range expectedNames {
		if strings.Contains(cmdline, name) {
			return true
		}
	}
	return false
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
