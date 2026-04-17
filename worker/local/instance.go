package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/util/naming"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

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

	// Prepare OCI bundle (no network/user namespace — inherited from pasta).
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

	logPath := filepath.Join(dir, "output.log")
	logWriter, err := newRotatingWriter(logPath)
	if err != nil {
		return fmt.Errorf("creating log file: %w", err)
	}

	// Build port forwards
	var forwards []runtime.PortForward
	for _, p := range manifest.Ports {
		forwards = append(forwards, runtime.PortForward{
			HostPort:      p.Port,
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		})
	}

	// Start container inside a pasta-managed network namespace.
	// pasta creates the namespace and runs crun inside it — no setns needed.
	handle, err := w.rt.StartContainerWithPasta(id, bundleDir, forwards, logWriter, logWriter)
	if err != nil {
		logWriter.Close()
		return fmt.Errorf("starting instance: %w", err)
	}

	inst := &managedInstance{
		id:        id,
		name:      manifest.Name,
		image:     manifest.Image,
		startedAt: time.Now(),
		logWriter: logWriter,
		done:      make(chan struct{}),
		handle: handle,
	}

	w.mu.Lock()
	w.instances[id] = inst
	w.mu.Unlock()

	state := instanceState{
		StartedAt: inst.startedAt,
	}
	if err := saveInstanceState(dir, state); err != nil {
		w.log.Warn("failed to persist instance state", "id", id, "error", err)
	}

	if w.tracker != nil {
		w.tracker.Track(id, manifest.Name)
		w.tracker.SetState(id, worker.StateRunning)

		logReader, err := w.InstanceLogs(context.Background(), id, 0, true)
		if err == nil {
			w.tracker.WatchLogs(context.Background(), id, readyPattern, logReader)
		}
	}

	// Exit watcher — the worker (inside pasta) handles crun delete
	go func() {
		exitCode := handle.Wait()
		inst.exitCode.Store(int32(exitCode))
		inst.exited.Store(true)
		if inst.logWriter != nil {
			inst.logWriter.Close()
		}

		uptime := time.Since(inst.startedAt)

		if exitCode != 0 && uptime < 3*time.Second {
			logData, _ := os.ReadFile(filepath.Join(w.instanceDir(id), "output.log"))
			w.log.Error("instance failed to start (exited immediately)",
				"id", id, "exit_code", exitCode, "uptime", uptime.Round(time.Millisecond),
				"output", truncate(string(logData), 500))
		} else {
			w.log.Info("instance exited", "id", id, "exit_code", exitCode, "uptime", uptime.Round(time.Second))
		}
		os.Remove(filepath.Join(w.instanceDir(id), "state.json"))
		close(inst.done)

		if w.tracker != nil {
			w.tracker.SetExited(id, int(inst.exitCode.Load()))
		}
	}()

	w.log.Info("instance started", "id", id, "pid", handle.PID, "image", manifest.Image)
	return nil
}

func (w *LocalWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	w.mu.Lock()
	inst, ok := w.instances[id]
	w.mu.Unlock()

	if !ok || inst.exited.Load() {
		return nil
	}

	w.log.Info("StopInstance called", "id", id)

	if err := inst.handle.Signal(syscall.SIGTERM); err != nil {
		w.log.Debug("signal SIGTERM failed", "id", id, "error", err)
	}

	select {
	case <-inst.done:
		return nil
	case <-time.After(time.Duration(timeoutSeconds) * time.Second):
		w.log.Warn("instance did not stop, killing", "id", id)
		inst.handle.Signal(syscall.SIGKILL)
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
		w.log.Warn("RemoveInstance: killing running instance", "id", id)
		inst.handle.Signal(syscall.SIGKILL)
		<-inst.done
	}

	if w.tracker != nil {
		w.tracker.Remove(id)
	}

	dir := w.instanceDir(id)
	os.RemoveAll(dir)
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

	// Not in memory — check crun state for instances surviving a restart
	cs, err := w.rt.State(id)
	if err != nil {
		return nil, fmt.Errorf("instance %s not found", id)
	}

	if cs.Status == "running" {
		dir := w.instanceDir(id)
		state, _ := loadInstanceState(dir)
		var startedAt time.Time
		if state != nil {
			startedAt = state.StartedAt
		}
		return &worker.InstanceInfo{
			ID:        id,
			State:     "running",
			StartedAt: startedAt,
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

	// Get the container PID from crun state for cgroup stats lookup
	containerPID := 0
	if inst.handle != nil {
		containerPID = inst.handle.PID
	}

	stats, err := readCgroupStats(containerPID)
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
			// Check crun state for instances not yet in memory
			if cs, err := w.rt.State(id); err == nil && cs.Status == "running" {
				state = "running"
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
