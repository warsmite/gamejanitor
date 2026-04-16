package local

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/warsmite/gamejanitor/worker"
)

// --- Recovery ---

// recoverInstances scans for active gj-*.scope systemd units and re-adopts
// instances that survived a gamejanitor restart.
func (w *LocalWorker) recoverInstances() {
	if w.rt == nil {
		return
	}

	containers, err := w.rt.List()
	if err != nil {
		w.log.Debug("could not list containers for recovery", "error", err)
		return
	}

	var recovered int
	for _, c := range containers {
		if c.Status != "running" {
			w.rt.Delete(c.ID, true)
			continue
		}

		id := c.ID
		dir := w.instanceDir(id)
		manifestData, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			w.log.Warn("running container has no manifest, killing", "id", id)
			w.rt.Kill(id, "SIGKILL")
			w.rt.Delete(id, true)
			continue
		}
		var manifest instanceManifest
		if json.Unmarshal(manifestData, &manifest) != nil {
			w.log.Warn("running container has corrupt manifest, killing", "id", id)
			w.rt.Kill(id, "SIGKILL")
			w.rt.Delete(id, true)
			continue
		}

		state, err := loadInstanceState(dir)
		if err != nil {
			w.log.Warn("running container has no state file, killing", "id", id)
			w.rt.Kill(id, "SIGKILL")
			w.rt.Delete(id, true)
			continue
		}

		unitName := "gj-" + id
		inst := &managedInstance{
			id:        id,
			name:      manifest.Name,
			image:     manifest.Image,
			startedAt: state.StartedAt,
			pid:       c.PID,
			done:      make(chan struct{}),
			unitName:  unitName,
		}

		w.mu.Lock()
		w.instances[id] = inst
		w.mu.Unlock()

		if w.tracker != nil {
			w.tracker.Recover(id, manifest.Name, worker.StateRunning, state.StartedAt, true)
		}

		go func(inst *managedInstance, dir string) {
			ticker := time.NewTicker(2 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				cs, err := w.rt.State(inst.id)
				if err != nil || cs.Status != "running" {
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
		w.log.Info("recovered running instance", "id", id, "pid", c.PID,
			"started_at", state.StartedAt)
	}

	// Clean up orphan systemd scopes that crun doesn't know about
	cleanupOrphanScopes(w.paths, w.log)

	if recovered > 0 {
		w.log.Info("instance recovery complete", "recovered", recovered)
	}
}
