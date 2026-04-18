package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// --- Recovery ---

// recoverInstances scans instance directories for containers that survived a
// gamejanitor restart. Uses persisted PIDs in state.json and checks process
// liveness directly — no crun list/state needed from outside the namespace.
func (w *LocalWorker) recoverInstances() {
	if w.rt == nil {
		return
	}

	instancesDir := filepath.Join(w.dataDir, "instances")
	entries, err := os.ReadDir(instancesDir)
	if err != nil {
		return
	}

	var recovered int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}

		id := e.Name()
		dir := filepath.Join(instancesDir, id)

		state, err := loadInstanceState(dir)
		if err != nil || state.ContainerPID == 0 {
			// No state file or no PID — instance was not running
			continue
		}

		// Check if the container init process is still alive
		if err := syscall.Kill(state.ContainerPID, 0); err == syscall.ESRCH {
			// Process is dead — clean up state file
			os.Remove(filepath.Join(dir, "state.json"))
			continue
		}

		manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			w.log.Warn("running container has no manifest, killing", "id", id)
			syscall.Kill(state.ContainerPID, syscall.SIGKILL)
			os.Remove(filepath.Join(dir, "state.json"))
			continue
		}
		var manifest instanceManifest
		if json.Unmarshal(manifestData, &manifest) != nil {
			w.log.Warn("running container has corrupt manifest, killing", "id", id)
			syscall.Kill(state.ContainerPID, syscall.SIGKILL)
			os.Remove(filepath.Join(dir, "state.json"))
			continue
		}

		// Restore port forwarding. If the old pasta survived, "Address in use"
		// is treated as success. If it died, a new pasta takes over.
		var forwards []runtime.PortForward
		for _, p := range manifest.Ports {
			forwards = append(forwards, runtime.PortForward{
				HostPort:      p.Port,
				ContainerPort: p.ContainerPort,
				Protocol:      p.Protocol,
			})
		}
		if err := w.rt.StartPasta(id, state.ContainerPID, forwards); err != nil {
			w.log.Warn("failed to restore port forwarding for recovered instance",
				"id", id, "error", err)
		}

		inst := &managedInstance{
			id:        id,
			name:      manifest.Name,
			image:     manifest.Image,
			startedAt: state.StartedAt,
			done:      make(chan struct{}),
			handle:    runtime.NewRecoveredHandle(state.ContainerPID),
		}

		w.mu.Lock()
		w.instances[id] = inst
		w.mu.Unlock()

		if w.tracker != nil {
			w.tracker.Recover(id, manifest.Name, worker.StateRunning, state.StartedAt, true)
		}

		// Poll for exit by checking process liveness
		go func(inst *managedInstance, dir string) {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if err := syscall.Kill(inst.handle.PID, 0); err == syscall.ESRCH {
					inst.exited.Store(true)
					inst.exitCode.Store(-1)
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
		w.log.Info("recovered running instance", "id", id,
			"container_pid", state.ContainerPID, "started_at", state.StartedAt)
	}

	if recovered > 0 {
		w.log.Info("instance recovery complete", "recovered", recovered)
	}
}
