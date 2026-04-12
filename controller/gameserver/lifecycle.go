package gameserver

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/utilities/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// Start transitions the gameserver to the running state. It validates
// preconditions synchronously, then spawns a goroutine to do the actual
// download/install/start work. Returns nil immediately on success.
func (g *LiveGameserver) Start(ctx context.Context) error {
	g.mu.Lock()

	if g.operation != nil {
		g.mu.Unlock()
		return fmt.Errorf("operation %s already in progress", g.operation.Type)
	}
	if g.desiredState == "archived" {
		g.mu.Unlock()
		return controller.ErrBadRequest("cannot start archived gameserver")
	}
	if g.worker == nil {
		g.mu.Unlock()
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", g.id)
	}
	if g.process != nil && g.process.State == worker.StateRunning {
		g.mu.Unlock()
		return nil
	}

	g.desiredState = "running"
	g.store.SetDesiredState(g.id, "running")
	g.errorReason = ""
	g.store.ClearErrorReason(g.id)
	g.crashCount = 0

	g.operation = &model.Operation{Type: model.OpStart, Phase: model.PhaseDownloadingGame}
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	go g.doStart(opCtx)
	return nil
}

func (g *LiveGameserver) doStart(ctx context.Context) {
	defer func() {
		g.mu.Lock()
		g.cancelOp = nil
		done := g.opDone
		g.opDone = nil
		g.mu.Unlock()
		if done != nil {
			close(done)
		}
	}()

	if err := g.executeStart(ctx); err != nil {
		g.log.Error("start failed", "gameserver", g.id, "error", err)
		g.clearOperation()
		return
	}
	// On success: don't clear the operation. StartInstance returns immediately
	// after launching the process — the worker event stream delivers StateRunning
	// when the ready pattern matches. HandleProcessEvent clears the operation then.
}

// executeStart performs the full start sequence: depot download, image pull,
// install phase (if needed), and instance creation.
func (g *LiveGameserver) executeStart(ctx context.Context) error {
	game := g.gameStore.GetGame(g.gameID)
	if game == nil {
		g.setError("Game not found: " + g.gameID)
		return fmt.Errorf("game %s not found", g.gameID)
	}

	w := g.getWorker()
	if w == nil {
		g.setError("Worker unavailable.")
		return fmt.Errorf("worker unavailable for gameserver %s", g.id)
	}

	// Download game files via Steam depot if the game requires it.
	var depotDir string
	depotAppID := game.DepotAppID()
	if depotAppID != 0 {
		accountName := ""
		refreshToken := ""
		if game.SteamLogin.RequiresAuth() {
			accountName = g.settingsSvc.GetString(settings.SettingSteamAccountName)
			refreshToken = g.settingsSvc.GetString(settings.SettingSteamRefreshToken)
			if refreshToken == "" {
				g.setError("This game requires a linked Steam account. Run 'gamejanitor steam login' to configure.")
				return fmt.Errorf("game %s requires Steam auth but no credentials configured", game.ID)
			}
		}

		g.bus.Publish(event.NewSystemEvent(event.EventDepotDownloading, g.id, &event.DepotDownloadingData{AppID: depotAppID}))
		g.setPhase(model.PhaseDownloadingGame)

		depotResult, depotErr := w.EnsureDepot(ctx, depotAppID, "public", accountName, refreshToken, func(p worker.DepotProgress) {
			if p.TotalBytes > 0 {
				g.setProgress(model.OperationProgress{
					Percent:        float64(p.CompletedBytes) / float64(p.TotalBytes) * 100,
					CompletedBytes: p.CompletedBytes,
					TotalBytes:     p.TotalBytes,
				})
			}
		})
		if depotErr != nil {
			g.setError("Failed to download game files from Steam.")
			return fmt.Errorf("depot download: %w", depotErr)
		}

		depotDir = depotResult.DepotDir

		if depotResult.Cached {
			g.bus.Publish(event.NewSystemEvent(event.EventDepotCached, g.id, &event.DepotCachedData{AppID: depotAppID}))
		} else {
			g.bus.Publish(event.NewSystemEvent(event.EventDepotComplete, g.id, &event.DepotCompleteData{
				AppID: depotAppID, BytesDownloaded: depotResult.BytesDownloaded,
			}))
		}
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Pull the OCI image.
	g.setPhase(model.PhasePullingImage)
	g.bus.Publish(event.NewSystemEvent(event.EventImagePulling, g.id, nil))

	gs := g.toModelGameserver()

	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env)), func(p worker.PullProgress) {
		if p.TotalBytes > 0 {
			g.setProgress(model.OperationProgress{
				Percent:        float64(p.CompletedBytes) / float64(p.TotalBytes) * 100,
				CompletedBytes: p.CompletedBytes,
				TotalBytes:     p.TotalBytes,
			})
		}
	}); err != nil {
		g.setError("Failed to pull game image. Check your internet connection.")
		return fmt.Errorf("pulling image: %w", err)
	}
	g.bus.Publish(event.NewSystemEvent(event.EventImagePulled, g.id, nil))

	if err := ctx.Err(); err != nil {
		return err
	}

	env, err := mergeEnv(game, &gs)
	if err != nil {
		g.setError("Failed to configure environment variables.")
		return fmt.Errorf("merging env: %w", err)
	}

	ports, err := parseGameserverPorts(&gs)
	if err != nil {
		g.setError("Invalid port configuration.")
		return fmt.Errorf("parsing ports: %w", err)
	}

	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, g.gameID, g.id)
	if err != nil {
		g.setError("Failed to extract game scripts.")
		return fmt.Errorf("preparing scripts: %w", err)
	}

	// Copy depot files into volume on first install (before the container runs).
	if depotDir != "" && !g.isInstalled() {
		g.log.Info("copying depot to volume", "depot", depotDir)
		if err := w.CopyDepotToVolume(ctx, depotDir, g.volumeName); err != nil {
			g.setError("Failed to copy game files to volume.")
			return fmt.Errorf("copying depot to volume: %w", err)
		}
	}

	if g.modReconciler != nil {
		if err := g.modReconciler.Reconcile(ctx, g.id); err != nil {
			g.log.Warn("mod reconciliation had errors, continuing", "error", err)
		}
	}

	binds := []string{scriptDir + ":/scripts:ro"}
	if defaultsDir != "" {
		binds = append(binds, defaultsDir+":/defaults:ro")
	}
	if depotDir != "" {
		binds = append(binds, depotDir+":/depot:ro")
	}

	// Clean up any stale instance from a previous run.
	instanceName := naming.InstanceName(g.id)
	g.mu.Lock()
	var oldInstanceID string
	if g.instanceID != nil {
		oldInstanceID = *g.instanceID
		g.instanceID = nil
	}
	g.mu.Unlock()
	if oldInstanceID != "" {
		g.store.SetInstanceID(g.id, nil)
		w.RemoveInstance(ctx, oldInstanceID)
	}
	w.RemoveInstance(ctx, instanceName)

	// Install phase — run install-server script if not yet installed.
	if !g.isInstalled() {
		g.setPhase(model.PhaseInstalling)

		rotateConsoleLogs(w, g.volumeName)
		copyDefaults(w, g.volumeName, defaultsDir)

		installName := naming.InstallInstanceName(g.id)
		w.RemoveInstance(ctx, installName)

		g.log.Info("running install phase")
		installID, installErr := w.CreateInstance(ctx, worker.InstanceOptions{
			Name:          installName,
			Image:         game.ResolveImage(map[string]string(gs.Env)),
			Env:           env,
			Ports:         ports,
			VolumeName:    g.volumeName,
			MemoryLimitMB: g.memoryLimitMB,
			CPULimit:      g.cpuLimit,
			CPUEnforced:   g.cpuEnforced,
			Binds:         binds,
			Entrypoint:    []string{"/bin/sh", "-c", "/scripts/install-server"},
		})
		if installErr != nil {
			g.setError(userFriendlyError("Failed to create install instance", installErr))
			return fmt.Errorf("creating install instance: %w", installErr)
		}

		if installErr = w.StartInstance(ctx, installID, ""); installErr != nil {
			w.RemoveInstance(ctx, installID)
			g.setError(userFriendlyError("Failed to start install instance", installErr))
			return fmt.Errorf("starting install instance: %w", installErr)
		}

		exitCode, installErr := waitForInstanceExit(ctx, w, installID)

		// Capture install output for the console log.
		if logReader, logErr := w.InstanceLogs(ctx, installID, 0, false); logErr == nil {
			logData, _ := io.ReadAll(logReader)
			logReader.Close()
			if len(logData) > 0 {
				w.WriteFile(ctx, g.volumeName, ".gamejanitor/logs/console.log", logData, 0644)
			}
			if exitCode != 0 || installErr != nil {
				out := string(logData)
				if len(out) > 500 {
					out = out[len(out)-500:]
				}
				g.log.Error("install phase failed", "exit_code", exitCode, "output", out)
			}
		}

		w.RemoveInstance(ctx, installID)

		if installErr != nil {
			g.setError("Install phase failed.")
			return fmt.Errorf("install phase: %w", installErr)
		}
		if exitCode != 0 {
			g.setError(fmt.Sprintf("Install script failed with exit code %d.", exitCode))
			return fmt.Errorf("install-server exited with code %d", exitCode)
		}

		g.mu.Lock()
		g.installed = true
		g.store.UpdateGameserver(g.toModelGameserverLocked())
		g.mu.Unlock()
		g.log.Info("install phase complete")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// Start the game server instance.
	g.setPhase(model.PhaseStarting)

	instanceID, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:          instanceName,
		Image:         game.ResolveImage(map[string]string(gs.Env)),
		Env:           env,
		Ports:         ports,
		VolumeName:    g.volumeName,
		MemoryLimitMB: g.memoryLimitMB,
		CPULimit:      g.cpuLimit,
		CPUEnforced:   g.cpuEnforced,
		Binds:         binds,
		Entrypoint:    []string{"/bin/sh", "-c", "exec /scripts/start-server"},
	})
	if err != nil {
		g.setError(userFriendlyError("Failed to create instance", err))
		return fmt.Errorf("creating instance: %w", err)
	}

	if ctx.Err() != nil {
		w.RemoveInstance(context.Background(), instanceID)
		return ctx.Err()
	}

	g.mu.Lock()
	g.instanceID = &instanceID
	g.appliedConfig = &model.AppliedConfig{
		Env:           g.env,
		MemoryLimitMB: g.memoryLimitMB,
		CPULimit:      g.cpuLimit,
		CPUEnforced:   g.cpuEnforced,
	}
	g.store.UpdateGameserver(g.toModelGameserverLocked())
	g.mu.Unlock()

	g.bus.Publish(event.NewSystemEvent(event.EventInstanceCreating, g.id, nil))

	if err := w.StartInstance(ctx, instanceID, game.ReadyPattern); err != nil {
		g.setError(userFriendlyError("Failed to start instance", err))
		return fmt.Errorf("starting instance: %w", err)
	}

	// StartInstance returns immediately — the process is starting but not yet ready.
	// The worker watches logs for the ready pattern and emits StateRunning when matched.
	// HandleProcessEvent will set process state and clear the operation.
	g.bus.Publish(event.NewSystemEvent(event.EventInstanceCreating, g.id, nil))
	g.log.Info("instance started, waiting for ready", "instance_id", instanceID[:12])
	return nil
}

// Stop transitions the gameserver to the stopped state. If an operation is in
// progress, it cancels it first and waits for the goroutine to finish.
func (g *LiveGameserver) Stop(ctx context.Context) error {
	g.mu.Lock()

	if g.instanceID == nil && g.process == nil && g.operation == nil {
		g.mu.Unlock()
		return nil
	}

	if g.cancelOp != nil {
		g.cancelOp()
	}
	done := g.opDone
	g.mu.Unlock()

	if done != nil {
		<-done
	}

	g.bus.Publish(event.NewSystemEvent(event.EventInstanceStopping, g.id, nil))
	return g.doStop(ctx)
}

// doStop performs the raw stop work: executes stop-server script, stops and
// removes the instance, and clears runtime state.
func (g *LiveGameserver) doStop(ctx context.Context) error {
	g.mu.Lock()
	instanceID := g.instanceID
	w := g.worker
	g.mu.Unlock()

	if instanceID != nil && w != nil {
		// Best-effort graceful stop via game script.
		execCtx, execCancel := context.WithTimeout(ctx, 15*time.Second)
		_, _, _, execErr := w.Exec(execCtx, *instanceID, []string{"/scripts/stop-server"})
		execCancel()
		if execErr != nil {
			g.log.Info("stop-server script not available or failed", "error", execErr)
		}

		stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
		if err := w.StopInstance(stopCtx, *instanceID, 10); err != nil {
			g.log.Warn("failed to stop instance gracefully", "error", err)
		}
		stopCancel()

		if err := w.RemoveInstance(ctx, *instanceID); err != nil {
			g.log.Warn("failed to remove instance", "error", err)
		}
	}

	g.mu.Lock()
	g.instanceID = nil
	g.desiredState = "stopped"
	g.process = nil
	g.errorReason = ""
	gsModel := g.toModelGameserverLocked()
	g.mu.Unlock()

	g.store.UpdateGameserver(gsModel)
	g.bus.Publish(event.NewSystemEvent(event.EventInstanceStopped, g.id, nil))
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, g.id, &event.StatusChangedData{
		Status: controller.StatusStopped,
	}))
	g.log.Info("gameserver stopped")
	return nil
}

// Restart stops the gameserver (if running) and starts it again. The entire
// sequence runs in a background goroutine.
func (g *LiveGameserver) Restart(ctx context.Context) error {
	g.mu.Lock()
	if g.operation != nil {
		g.mu.Unlock()
		return fmt.Errorf("operation %s already in progress", g.operation.Type)
	}
	if g.worker == nil {
		g.mu.Unlock()
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", g.id)
	}
	g.operation = &model.Operation{Type: model.OpRestart, Phase: model.PhaseStopping}
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			g.cancelOp = nil
			done := g.opDone
			g.opDone = nil
			g.mu.Unlock()
			if done != nil {
				close(done)
			}
		}()

		if err := g.stopIfRunning(opCtx); err != nil {
			g.log.Error("restart stop phase failed", "error", err)
			g.clearOperation()
			return
		}

		if err := g.executeStart(opCtx); err != nil {
			if opCtx.Err() == nil {
				g.setError(fmt.Sprintf("Restart failed: %v", err))
			}
			g.clearOperation()
		}
		// On success: HandleProcessEvent clears the operation when worker reports ready
	}()

	return nil
}

// UpdateServerGame stops the gameserver, pulls the latest image, runs the
// update-server script, and restarts. Runs in a background goroutine.
func (g *LiveGameserver) UpdateServerGame(ctx context.Context) error {
	g.mu.Lock()
	if g.operation != nil {
		g.mu.Unlock()
		return fmt.Errorf("operation %s already in progress", g.operation.Type)
	}
	if g.worker == nil {
		g.mu.Unlock()
		return controller.ErrUnavailablef("worker unavailable")
	}
	g.operation = &model.Operation{Type: model.OpUpdate, Phase: model.PhaseStopping}
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			g.cancelOp = nil
			done := g.opDone
			g.opDone = nil
			g.mu.Unlock()
			if done != nil {
				close(done)
			}
		}()

		if err := g.executeUpdateGame(opCtx); err != nil {
			if opCtx.Err() == nil {
				g.setError(fmt.Sprintf("Game update failed: %v", err))
			}
			g.clearOperation()
		}
		// On success: HandleProcessEvent clears the operation when worker reports ready
	}()

	return nil
}

func (g *LiveGameserver) executeUpdateGame(ctx context.Context) error {
	game := g.gameStore.GetGame(g.gameID)
	if game == nil {
		g.setError("Game not found")
		return fmt.Errorf("game %s not found", g.gameID)
	}

	g.bus.Publish(event.NewSystemEvent(event.EventImagePulling, g.id, nil))
	if err := g.stopIfRunning(ctx); err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return err
	}

	w := g.getWorker()
	if w == nil {
		g.setError(controller.OperationFailedReason("Game update failed", fmt.Errorf("worker unavailable")))
		return fmt.Errorf("worker unavailable")
	}

	gs := g.toModelGameserver()

	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env)), nil); err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("pulling image for update: %w", err)
	}

	scriptDir, _, err := w.PrepareGameScripts(ctx, g.gameID, g.id)
	if err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("preparing scripts: %w", err)
	}

	env, err := mergeEnv(game, &gs)
	if err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return err
	}

	tempName := naming.UpdateInstanceName(g.id)
	tempID, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       tempName,
		Image:      game.ResolveImage(map[string]string(gs.Env)),
		Env:        env,
		VolumeName: g.volumeName,
		Binds:      []string{scriptDir + ":/scripts:ro"},
		Entrypoint: []string{"/bin/sh", "-c", "/scripts/update-server"},
	})
	if err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("creating update instance: %w", err)
	}
	defer w.RemoveInstance(ctx, tempID)

	if err := w.StartInstance(ctx, tempID, ""); err != nil {
		g.setError(controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("starting update instance: %w", err)
	}

	exitCode, waitErr := waitForInstanceExit(ctx, w, tempID)
	if waitErr != nil {
		g.setError(controller.OperationFailedReason("Game update failed", waitErr))
		return waitErr
	}
	if exitCode != 0 {
		g.setError(controller.OperationFailedReason("Game update failed", fmt.Errorf("exit code %d", exitCode)))
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	g.log.Info("game updated, starting")
	return g.executeStart(ctx)
}

// Reinstall stops the gameserver, wipes its volume, and performs a fresh
// install+start. Runs in a background goroutine.
func (g *LiveGameserver) Reinstall(ctx context.Context) error {
	g.mu.Lock()
	if g.operation != nil {
		g.mu.Unlock()
		return fmt.Errorf("operation %s already in progress", g.operation.Type)
	}
	if g.worker == nil {
		g.mu.Unlock()
		return controller.ErrUnavailablef("worker unavailable")
	}
	g.operation = &model.Operation{Type: model.OpReinstall, Phase: model.PhaseStopping}
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	go func() {
		defer func() {
			g.mu.Lock()
			g.cancelOp = nil
			done := g.opDone
			g.opDone = nil
			g.mu.Unlock()
			if done != nil {
				close(done)
			}
		}()

		if err := g.executeReinstall(opCtx); err != nil {
			if opCtx.Err() == nil {
				g.setError(fmt.Sprintf("Reinstall failed: %v", err))
			}
			g.clearOperation()
		}
		// On success: HandleProcessEvent clears the operation when worker reports ready
	}()

	return nil
}

func (g *LiveGameserver) executeReinstall(ctx context.Context) error {
	g.bus.Publish(event.NewSystemEvent(event.EventImagePulling, g.id, nil))

	if err := g.stopIfRunning(ctx); err != nil {
		g.setError(controller.OperationFailedReason("Reinstall failed", err))
		return err
	}

	w := g.getWorker()
	if w == nil {
		g.setError(controller.OperationFailedReason("Reinstall failed", fmt.Errorf("worker unavailable")))
		return fmt.Errorf("worker unavailable")
	}

	g.mu.Lock()
	g.installed = false
	g.store.UpdateGameserver(g.toModelGameserverLocked())
	g.mu.Unlock()

	if err := w.RemoveVolume(ctx, g.volumeName); err != nil {
		g.setError(controller.OperationFailedReason("Reinstall failed", err))
		return err
	}
	if err := w.CreateVolume(ctx, g.volumeName); err != nil {
		g.setError(controller.OperationFailedReason("Reinstall failed", err))
		return err
	}

	g.log.Info("volume wiped, starting fresh install")
	return g.executeStart(ctx)
}

// --- Helper methods ---

// getWorker returns the current worker reference (thread-safe read).
func (g *LiveGameserver) getWorker() worker.Worker {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.worker
}

// isInstalled returns the installed flag (thread-safe read).
func (g *LiveGameserver) isInstalled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.installed
}

// toModelGameserver creates a model.Gameserver from the live state. Acquires g.mu.
func (g *LiveGameserver) toModelGameserver() model.Gameserver {
	g.mu.Lock()
	defer g.mu.Unlock()
	return *g.toModelGameserverLocked()
}

// toModelGameserverLocked creates a model.Gameserver from the live state.
// Caller must hold g.mu.
func (g *LiveGameserver) toModelGameserverLocked() *model.Gameserver {
	return &model.Gameserver{
		ID:                 g.id,
		Name:               g.name,
		GameID:             g.gameID,
		Ports:              g.ports,
		Env:                g.env,
		MemoryLimitMB:      g.memoryLimitMB,
		CPULimit:           g.cpuLimit,
		CPUEnforced:        g.cpuEnforced,
		InstanceID:         g.instanceID,
		VolumeName:         g.volumeName,
		PortMode:           g.portMode,
		NodeID:             g.nodeID,
		SFTPUsername:        g.sftpUsername,
		HashedSFTPPassword: g.hashedSFTPPassword,
		Installed:          g.installed,
		BackupLimit:        g.backupLimit,
		StorageLimitMB:     g.storageLimitMB,
		NodeTags:           g.nodeTags,
		AutoRestart:        g.autoRestart,
		ConnectionAddress:  g.connectionAddress,
		AppliedConfig:      g.appliedConfig,
		DesiredState:       g.desiredState,
		ErrorReason:        g.errorReason,
		CreatedByTokenID:   g.createdByTokenID,
		Grants:             g.grants,
		CreatedAt:          g.createdAt,
		UpdatedAt:          g.updatedAt,
	}
}

// setError acquires g.mu and sets the error reason, persists it, and publishes
// an error event.
func (g *LiveGameserver) setError(reason string) {
	g.mu.Lock()
	g.setErrorLocked(reason)
	g.mu.Unlock()
}

// waitForInstanceExit polls instance state until it exits or the context is cancelled.
func waitForInstanceExit(ctx context.Context, w worker.Worker, instanceID string) (int, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-ticker.C:
			info, err := w.InspectInstance(ctx, instanceID)
			if err != nil {
				return -1, fmt.Errorf("inspecting instance %s: %w", instanceID, err)
			}
			if info.State == "exited" || info.State == "created" {
				return info.ExitCode, nil
			}
		}
	}
}

// rotateConsoleLogs rotates .gamejanitor/logs/console.log through .0 → .1 → .2 → .3,
// keeping the last 4 log files.
func rotateConsoleLogs(w worker.Worker, volumeName string) {
	ctx := context.Background()
	logDir := ".gamejanitor/logs"
	w.CreateDirectory(ctx, volumeName, logDir)
	for i := 2; i >= 0; i-- {
		from := fmt.Sprintf("%s/console.log.%d", logDir, i)
		to := fmt.Sprintf("%s/console.log.%d", logDir, i+1)
		w.RenamePath(ctx, volumeName, from, to)
	}
	w.RenamePath(ctx, volumeName, logDir+"/console.log", logDir+"/console.log.0")
}

// copyDefaults copies default config files from the game definition into the
// volume root, skipping any files that already exist.
func copyDefaults(w worker.Worker, volumeName string, defaultsDir string) {
	if defaultsDir == "" {
		return
	}
	ctx := context.Background()
	entries, err := os.ReadDir(defaultsDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		destPath := "/" + entry.Name()
		// Skip if file already exists in the volume.
		if _, err := w.ReadFile(ctx, volumeName, destPath); err == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(defaultsDir, entry.Name()))
		if err != nil {
			continue
		}
		w.WriteFile(ctx, volumeName, destPath, data, 0644)
	}
}
