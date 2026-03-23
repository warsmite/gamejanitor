package worker

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

	"github.com/warsmite/gamejanitor/games"
)

// ProcessWorker implements Worker by running game servers as bare processes.
// Images are pulled via OCI and extracted to disk; no container runtime required.
type ProcessWorker struct {
	log       *slog.Logger
	gameStore *games.GameStore
	dataDir   string
	resolve   volumeResolver

	mu        sync.Mutex
	processes map[string]*managedProcess

	eventMu     sync.Mutex
	eventCh     chan ContainerEvent
	eventActive bool
}

type managedProcess struct {
	cmd       *exec.Cmd
	id        string
	name      string
	image     string
	startedAt time.Time
	exitCode  int
	exited    bool
	logFile   *os.File
	done      chan struct{}
}

// processManifest is persisted to disk so StartContainer can find the config.
type processManifest struct {
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Env        []string `json:"env"`
	VolumeName string   `json:"volume_name"`
	Binds      []string `json:"binds"`
}

func NewProcessWorker(gameStore *games.GameStore, dataDir string, log *slog.Logger) *ProcessWorker {
	w := &ProcessWorker{
		log:       log,
		gameStore: gameStore,
		dataDir:   dataDir,
		processes: make(map[string]*managedProcess),
		eventCh:   make(chan ContainerEvent, 64),
	}
	w.resolve = w.processVolumeResolver()
	log.Info("using process runtime (no container isolation)")
	return w
}

func (w *ProcessWorker) processVolumeResolver() volumeResolver {
	return func(ctx context.Context, volumeName string) (string, error) {
		path := filepath.Join(w.dataDir, "volumes", volumeName)
		if _, err := os.Stat(path); err != nil {
			return "", fmt.Errorf("volume %s not found: %w", volumeName, err)
		}
		return path, nil
	}
}

func (w *ProcessWorker) imagesDir() string {
	return filepath.Join(w.dataDir, "images")
}

func (w *ProcessWorker) processDir(id string) string {
	return filepath.Join(w.dataDir, "processes", id)
}

// --- Worker interface implementation ---

func (w *ProcessWorker) PullImage(ctx context.Context, image string) error {
	_, err := pullAndExtractOCIImage(ctx, image, w.imagesDir(), w.log)
	return err
}

func (w *ProcessWorker) CreateContainer(ctx context.Context, opts ContainerOptions) (string, error) {
	id := opts.Name // reuse the container name as the process ID

	dir := w.processDir(id)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("creating process dir: %w", err)
	}

	manifest := processManifest{
		Name:       opts.Name,
		Image:      opts.Image,
		Env:        opts.Env,
		VolumeName: opts.VolumeName,
		Binds:      opts.Binds,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("marshaling process manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0644); err != nil {
		return "", fmt.Errorf("writing process manifest: %w", err)
	}

	w.log.Info("process created", "id", id, "image", opts.Image)
	return id, nil
}

func (w *ProcessWorker) StartContainer(ctx context.Context, id string) error {
	dir := w.processDir(id)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("reading process manifest: %w", err)
	}

	var manifest processManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("parsing process manifest: %w", err)
	}

	// Resolve extracted image rootfs
	rootFS, imgCfg, err := imageRootFS(w.imagesDir(), manifest.Image)
	if err != nil {
		return fmt.Errorf("resolving image rootfs: %w", err)
	}

	// Build command from image entrypoint + cmd
	cmdArgs := append(imgCfg.Entrypoint, imgCfg.Cmd...)
	if len(cmdArgs) == 0 {
		return fmt.Errorf("image %s has no entrypoint or cmd", manifest.Image)
	}

	// Build bwrap command: mount extracted rootfs as /, bind volume to /data
	bwrapArgs := buildBwrapArgs(rootFS, manifest, imgCfg)
	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, cmdArgs...)

	cmd := exec.Command("bwrap", bwrapArgs...)

	// Set up log file
	logPath := filepath.Join(dir, "output.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("starting process: %w", err)
	}

	proc := &managedProcess{
		cmd:       cmd,
		id:        id,
		name:      manifest.Name,
		image:     manifest.Image,
		startedAt: time.Now(),
		logFile:   logFile,
		done:      make(chan struct{}),
	}

	w.mu.Lock()
	w.processes[id] = proc
	w.mu.Unlock()

	// Exit watcher
	go func() {
		cmd.Wait()
		proc.exitCode = cmd.ProcessState.ExitCode()
		proc.exited = true
		logFile.Close()

		w.log.Info("process exited", "id", id, "exit_code", proc.exitCode)
		close(proc.done)

		w.eventMu.Lock()
		active := w.eventActive
		w.eventMu.Unlock()

		if active {
			select {
			case w.eventCh <- ContainerEvent{
				ContainerID:   id,
				ContainerName: manifest.Name,
				Action:        "die",
			}:
			default:
			}
		}
	}()

	w.log.Info("process started", "id", id, "pid", cmd.Process.Pid, "image", manifest.Image)
	return nil
}

func (w *ProcessWorker) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	w.mu.Lock()
	proc, ok := w.processes[id]
	w.mu.Unlock()

	if !ok || proc.exited {
		return nil
	}

	// Send SIGTERM
	if proc.cmd.Process != nil {
		proc.cmd.Process.Signal(syscall.SIGTERM)
	}

	// Wait for graceful shutdown or timeout
	select {
	case <-proc.done:
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("process did not stop gracefully, sending SIGKILL", "id", id)
		if proc.cmd.Process != nil {
			proc.cmd.Process.Kill()
		}
		<-proc.done
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *ProcessWorker) RemoveContainer(ctx context.Context, id string) error {
	w.mu.Lock()
	proc, ok := w.processes[id]
	if ok {
		delete(w.processes, id)
	}
	w.mu.Unlock()

	// Kill if still running
	if ok && !proc.exited && proc.cmd.Process != nil {
		proc.cmd.Process.Kill()
		<-proc.done
	}

	// Clean up process directory
	dir := w.processDir(id)
	os.RemoveAll(dir)
	return nil
}

func (w *ProcessWorker) InspectContainer(ctx context.Context, id string) (*ContainerInfo, error) {
	w.mu.Lock()
	proc, ok := w.processes[id]
	w.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("process %s not found", id)
	}

	state := "running"
	if proc.exited {
		state = "exited"
	}

	return &ContainerInfo{
		ID:        proc.id,
		State:     state,
		StartedAt: proc.startedAt,
		ExitCode:  proc.exitCode,
	}, nil
}

func (w *ProcessWorker) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok {
		return -1, "", "", fmt.Errorf("process %s not found", containerID)
	}

	// Read manifest to rebuild bwrap args
	dir := w.processDir(containerID)
	manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return -1, "", "", fmt.Errorf("reading process manifest: %w", err)
	}
	var manifest processManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return -1, "", "", fmt.Errorf("parsing process manifest: %w", err)
	}

	rootFS, imgCfg, err := imageRootFS(w.imagesDir(), proc.image)
	if err != nil {
		return -1, "", "", fmt.Errorf("resolving image rootfs for exec: %w", err)
	}

	bwrapArgs := buildBwrapArgs(rootFS, manifest, imgCfg)
	bwrapArgs = append(bwrapArgs, "--")
	bwrapArgs = append(bwrapArgs, cmd...)

	execCmd := exec.CommandContext(ctx, "bwrap", bwrapArgs...)

	var stdout, stderr bytes.Buffer
	execCmd.Stdout = &stdout
	execCmd.Stderr = &stderr

	err = execCmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return -1, "", "", err
		}
	}

	return exitCode, stdout.String(), stderr.String(), nil
}

func (w *ProcessWorker) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	dir := w.processDir(containerID)
	logPath := filepath.Join(dir, "output.log")

	f, err := os.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("opening log file: %w", err)
	}

	if tail > 0 && !follow {
		lines, err := tailFile(f, tail)
		if err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
		return io.NopCloser(strings.NewReader(strings.Join(lines, "\n") + "\n")), nil
	}

	if follow {
		return newFollowReader(ctx, f, containerID, w), nil
	}

	return f, nil
}

func (w *ProcessWorker) ContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok || proc.exited || proc.cmd.Process == nil {
		return &ContainerStats{}, nil
	}

	pid := proc.cmd.Process.Pid
	memBytes, err := readProcMemory(pid)
	if err != nil {
		return &ContainerStats{}, nil
	}

	cpuPercent, err := readProcCPU(pid)
	if err != nil {
		cpuPercent = 0
	}

	return &ContainerStats{
		MemoryUsageMB: int(memBytes / (1024 * 1024)),
		CPUPercent:    cpuPercent,
	}, nil
}

// --- Volume operations ---

func (w *ProcessWorker) CreateVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	return os.MkdirAll(path, 0755)
}

func (w *ProcessWorker) RemoveVolume(ctx context.Context, name string) error {
	path := filepath.Join(w.dataDir, "volumes", name)
	return os.RemoveAll(path)
}

func (w *ProcessWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	return volumeSizeDirect(w.resolve, ctx, volumeName)
}

// --- File operations (all direct filesystem) ---

func (w *ProcessWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	return listFilesDirect(w.resolve, ctx, volumeName, path)
}

func (w *ProcessWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	return readFileDirect(w.resolve, ctx, volumeName, path)
}

func (w *ProcessWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	return writeFileDirect(w.resolve, ctx, volumeName, path, content, perm)
}

func (w *ProcessWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	return deletePathDirect(w.resolve, ctx, volumeName, path)
}

func (w *ProcessWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	return createDirectoryDirect(w.resolve, ctx, volumeName, path)
}

func (w *ProcessWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	return renamePathDirect(w.resolve, ctx, volumeName, from, to)
}

// --- Copy operations (direct filesystem for process worker) ---

func (w *ProcessWorker) CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error) {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("process %s not found", containerID)
	}

	fullPath := filepath.Join(proc.cmd.Dir, path)
	return os.ReadFile(fullPath)
}

func (w *ProcessWorker) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok {
		return fmt.Errorf("process %s not found", containerID)
	}

	fullPath := filepath.Join(proc.cmd.Dir, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, content, 0644)
}

func (w *ProcessWorker) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("process %s not found", containerID)
	}

	fullPath := filepath.Join(proc.cmd.Dir, path)
	return tarDirectory(fullPath)
}

func (w *ProcessWorker) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	w.mu.Lock()
	proc, ok := w.processes[containerID]
	w.mu.Unlock()

	if !ok {
		return fmt.Errorf("process %s not found", containerID)
	}

	fullPath := filepath.Join(proc.cmd.Dir, destPath)
	return extractTar(fullPath, content)
}

// --- Backup/Restore ---

func (w *ProcessWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	return backupVolumeDirect(w.resolve, ctx, volumeName)
}

func (w *ProcessWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	return restoreVolumeDirect(w.resolve, ctx, volumeName, tarStream)
}

// --- Events ---

func (w *ProcessWorker) WatchEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
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

// --- Game scripts ---

func (w *ProcessWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return prepareGameScripts(w.gameStore, w.dataDir, gameID, gameserverID)
}

// --- Helpers ---

// buildBwrapArgs constructs bubblewrap arguments to run a process inside the extracted rootfs.
// The rootfs is mounted as /, volumes are bound to /data, and scripts are bound to /scripts.
func buildBwrapArgs(rootFS string, manifest processManifest, imgCfg *imageConfig) []string {
	args := []string{
		"--bind", rootFS, "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--die-with-parent",
	}

	// Bind host DNS config into the sandbox
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}

	// Bind host SSL certs only if the rootfs doesn't have its own
	rootFSCerts := filepath.Join(rootFS, "etc/ssl/certs/ca-certificates.crt")
	if _, err := os.Stat(rootFSCerts); err != nil {
		for _, certPath := range []struct{ src, dst string }{
			{"/etc/ssl/certs", "/etc/ssl/certs"},
			{"/etc/pki/tls/certs", "/etc/pki/tls/certs"},
		} {
			if _, err := os.Stat(certPath.src); err == nil {
				args = append(args, "--ro-bind", certPath.src, certPath.dst)
			}
		}
	}

	// Bind volume to /data
	if manifest.VolumeName != "" {
		// Volume path is already on disk at dataDir/volumes/<name>
		// We need to figure it out from the manifest — the volume dir is a sibling of processes/
		// The ProcessWorker stores volumes at <dataDir>/volumes/<volumeName>
		// Since we don't have dataDir here, extract it from rootFS's parent structure
		// rootFS is <dataDir>/images/<algo>/<hash>, so dataDir is 3 levels up
		dataDir := filepath.Dir(filepath.Dir(filepath.Dir(rootFS)))
		volumeDir := filepath.Join(dataDir, "volumes", manifest.VolumeName)
		args = append(args, "--bind", volumeDir, "/data")
	}

	// Bind-mount any host paths (scripts, defaults)
	for _, bind := range manifest.Binds {
		parts := strings.SplitN(bind, ":", 3)
		if len(parts) >= 2 {
			hostPath := parts[0]
			containerPath := parts[1]
			if len(parts) == 3 && strings.Contains(parts[2], "ro") {
				args = append(args, "--ro-bind", hostPath, containerPath)
			} else {
				args = append(args, "--bind", hostPath, containerPath)
			}
		}
	}

	// Set working directory
	if imgCfg.WorkingDir != "" {
		args = append(args, "--chdir", imgCfg.WorkingDir)
	} else {
		args = append(args, "--chdir", "/data")
	}

	// Pass environment variables
	for _, e := range imgCfg.Env {
		args = append(args, "--setenv", envKey(e), envVal(e))
	}
	for _, e := range manifest.Env {
		args = append(args, "--setenv", envKey(e), envVal(e))
	}

	// Ensure HOME is set — many tools expect it and bwrap doesn't inherit host env
	args = append(args, "--setenv", "HOME", "/tmp")

	return args
}

func envKey(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[:i]
	}
	return kv
}

func envVal(kv string) string {
	if i := strings.IndexByte(kv, '='); i >= 0 {
		return kv[i+1:]
	}
	return ""
}

// tailFile reads the last n lines from a file.
func tailFile(f *os.File, n int) ([]string, error) {
	scanner := bufio.NewScanner(f)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, scanner.Err()
}

// followReader wraps a file for tailing with follow support.
type followReader struct {
	f           *os.File
	ctx         context.Context
	containerID string
	worker      *ProcessWorker
}

func newFollowReader(ctx context.Context, f *os.File, containerID string, w *ProcessWorker) *followReader {
	return &followReader{f: f, ctx: ctx, containerID: containerID, worker: w}
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

		// Check if process is still running
		r.worker.mu.Lock()
		proc, ok := r.worker.processes[r.containerID]
		r.worker.mu.Unlock()
		if !ok || proc.exited {
			// Read any remaining data
			n, _ = r.f.Read(p)
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}

		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *followReader) Close() error {
	return r.f.Close()
}

// readProcMemory reads RSS memory in bytes from /proc/<pid>/status.
func readProcMemory(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, err := strconv.ParseInt(fields[1], 10, 64)
				if err != nil {
					return 0, err
				}
				return kb * 1024, nil
			}
		}
	}
	return 0, fmt.Errorf("VmRSS not found in /proc/%d/status", pid)
}

// readProcCPU returns a rough CPU usage percentage for a process.
func readProcCPU(pid int) (float64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}

	fields := strings.Fields(string(data))
	if len(fields) < 17 {
		return 0, fmt.Errorf("unexpected /proc/stat format")
	}

	utime, _ := strconv.ParseFloat(fields[13], 64)
	stime, _ := strconv.ParseFloat(fields[14], 64)
	starttime, _ := strconv.ParseFloat(fields[21], 64)

	uptime, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	uptimeSeconds, _ := strconv.ParseFloat(strings.Fields(string(uptime))[0], 64)

	clkTck := float64(100) // sysconf(_SC_CLK_TCK), almost always 100 on Linux
	totalTime := (utime + stime) / clkTck
	elapsed := uptimeSeconds - (starttime / clkTck)
	if elapsed <= 0 {
		return 0, nil
	}

	return (totalTime / elapsed) * 100, nil
}

// tarDirectory creates a tar stream from a directory.
func tarDirectory(path string) (io.ReadCloser, error) {
	// Reuse the backup helper with a simple resolver
	resolve := func(_ context.Context, _ string) (string, error) {
		return path, nil
	}
	return backupVolumeDirect(resolve, context.Background(), "")
}

// extractTar extracts a tar stream to a directory.
func extractTar(destPath string, content io.Reader) error {
	resolve := func(_ context.Context, _ string) (string, error) {
		return destPath, nil
	}
	return restoreVolumeDirect(resolve, context.Background(), "", content)
}
