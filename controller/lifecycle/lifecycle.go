package lifecycle

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// Start validates preconditions and starts a gameserver. Blocks until the
// gameserver is running or an error occurs. Resets the crash counter so
// auto-restart gets a fresh retry budget.
func (s *Service) Start(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
	if s.statusProvider != nil {
		s.statusProvider.ResetCrashCount(id)
	}
	return s.startInstance(ctx, id, onProgress)
}

// RestartAfterCrash is called by the auto-restart system. Unlike Start(),
// it does NOT reset the crash counter — the counter must accumulate across
// retries so the 3-attempt limit works.
func (s *Service) RestartAfterCrash(ctx context.Context, id string) error {
	return s.startInstance(ctx, id, nil)
}

// startInstance performs the full start sequence: validate → auto-migrate if
// needed → download depot → pull image → install phase → start instance.
func (s *Service) startInstance(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	switch gs.Status {
	case controller.StatusInstalling, controller.StatusStarting, controller.StatusRunning:
		s.log.Info("gameserver already active, skipping start", "gameserver", id, "status", gs.Status)
		return nil
	}

	// Clear stale error/worker state only when we're actually going to start.
	// Must be after the guard — ClearError wipes the worker state cache, and
	// calling it on an already-running server would lose the "running" state.
	if s.statusProvider != nil {
		s.statusProvider.ClearError(id)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return controller.ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, id)
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	// Check if assigned node can fit this gameserver's resources.
	// If not, auto-migrate to a node that can before starting.
	if gs.NodeID != nil && *gs.NodeID != "" {
		limitErr := s.placement.CheckWorkerLimitsExcluding(*gs.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB), gs.ID)
		if limitErr != nil {
			s.log.Warn("assigned node cannot fit gameserver resources, attempting auto-migration",
				"gameserver", id, "node_id", *gs.NodeID, "error", limitErr)

			foundNode, findErr := s.placement.FindNodeWithCapacity(gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB), gs.NodeTags, *gs.NodeID)
			if findErr != nil {
				return fmt.Errorf("cannot start: node %s lacks capacity and no other node can fit %d MB / %.1f CPU", *gs.NodeID, gs.MemoryLimitMB, gs.CPULimit)
			}

			s.log.Info("auto-migrating before start", "gameserver", id, "from_node", *gs.NodeID, "to_node", foundNode)
			if err := s.doMigrate(ctx, id, foundNode); err != nil {
				return fmt.Errorf("auto-migration before start failed: %w", err)
			}

			gs, err = s.store.GetGameserver(id)
			if err != nil || gs == nil {
				return fmt.Errorf("reloading gameserver after migration: %w", err)
			}
			w = s.dispatcher.WorkerFor(id)
			if w == nil {
				return controller.ErrUnavailablef("worker unavailable after migration for gameserver %s", id)
			}
		}
	}

	// Set desired state immediately so DeriveStatus reflects intent
	gs.DesiredState = "running"
	s.store.UpdateGameserver(gs)

	// Re-read for the heavy work
	gs, err = s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	game = s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return fmt.Errorf("game %s not found for gameserver %s", gs.GameID, id)
	}

	w = s.dispatcher.WorkerFor(id)
	if w == nil {
		s.setError(id, "Worker became unavailable during start.")
		return fmt.Errorf("worker unavailable for gameserver %s", id)
	}

	// Download game files via Steam depot downloader for all Steam games.
	var depotDir string
	depotAppID := game.DepotAppID()
	if depotAppID != 0 {
		accountName := ""
		refreshToken := ""

		if game.SteamLogin.RequiresAuth() {
			accountName = s.settingsSvc.GetString(settings.SettingSteamAccountName)
			refreshToken = s.settingsSvc.GetString(settings.SettingSteamRefreshToken)
			if refreshToken == "" {
				s.setError(id, "This game requires a linked Steam account. Run 'gamejanitor steam login' to configure.")
				return fmt.Errorf("game %s requires Steam auth but no credentials configured", game.ID)
			}
		}

		s.broadcaster.Publish(event.NewSystemEvent(event.EventDepotDownloading, id, &event.DepotDownloadingData{
			AppID: depotAppID,
		}))
		reportProgress(onProgress, model.PhaseDownloadingGame, nil)

		depotResult, depotErr := w.EnsureDepot(ctx, depotAppID, "public", accountName, refreshToken, func(p worker.DepotProgress) {
			if p.TotalBytes > 0 {
				reportProgress(onProgress, model.PhaseDownloadingGame, &model.OperationProgress{
					Percent:        float64(p.CompletedBytes) / float64(p.TotalBytes) * 100,
					CompletedBytes: p.CompletedBytes,
					TotalBytes:     p.TotalBytes,
				})
			}
		})
		if depotErr != nil {
			s.setError(id, "Failed to download game files from Steam.")
			return fmt.Errorf("depot download for gameserver %s: %w", id, depotErr)
		}

		depotDir = depotResult.DepotDir

		if depotResult.Cached {
			s.broadcaster.Publish(event.NewSystemEvent(event.EventDepotCached, id, &event.DepotCachedData{
				AppID: depotAppID,
			}))
		} else {
			s.broadcaster.Publish(event.NewSystemEvent(event.EventDepotComplete, id, &event.DepotCompleteData{
				AppID:           depotAppID,
				BytesDownloaded: depotResult.BytesDownloaded,
			}))
		}
	}

	// Pull image
	reportProgress(onProgress, model.PhasePullingImage, nil)
	s.broadcaster.Publish(event.NewSystemEvent(event.EventImagePulling, id, nil))
	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env)), func(p worker.PullProgress) {
		if p.TotalBytes > 0 {
			reportProgress(onProgress, model.PhasePullingImage, &model.OperationProgress{
				Percent:        float64(p.CompletedBytes) / float64(p.TotalBytes) * 100,
				CompletedBytes: p.CompletedBytes,
				TotalBytes:     p.TotalBytes,
			})
		}
	}); err != nil {
		s.setError(id, "Failed to pull game image. Check your internet connection.")
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}
	s.broadcaster.Publish(event.NewSystemEvent(event.EventImagePulled, id, nil))

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		s.setError(id, "Failed to configure environment variables.")
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		s.setError(id, "Invalid port configuration.")
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Prepare game scripts on the target worker
	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		s.setError(id, "Failed to extract game scripts.")
		return fmt.Errorf("preparing scripts for gameserver %s: %w", id, err)
	}

	// Copy depot files into the volume on the host (outside the instance).
	if depotDir != "" && !gs.Installed {
		s.log.Info("copying depot to volume", "gameserver", id, "depot", depotDir)
		if err := w.CopyDepotToVolume(ctx, depotDir, gs.VolumeName); err != nil {
			s.setError(id, "Failed to copy game files to volume.")
			return fmt.Errorf("copying depot to volume for gameserver %s: %w", id, err)
		}
	}

	// Reconcile mods — ensure DB-tracked mods exist on the volume before starting.
	if s.modReconciler != nil {
		if err := s.modReconciler.Reconcile(ctx, id); err != nil {
			s.log.Warn("mod reconciliation had errors, continuing with start", "gameserver", id, "error", err)
		}
	}

	binds := []string{
		scriptDir + ":/scripts:ro",
	}
	if defaultsDir != "" {
		binds = append(binds, defaultsDir+":/defaults:ro")
	}
	if depotDir != "" {
		binds = append(binds, depotDir+":/depot:ro")
	}

	// Remove old instance if exists (stale from prior run/crash).
	instanceName := naming.InstanceName(id)
	if gs.InstanceID != nil {
		oldID := *gs.InstanceID
		gs.InstanceID = nil
		if err := s.store.UpdateGameserver(gs); err != nil {
			s.log.Warn("failed to clear old instance ID", "gameserver", id, "error", err)
		}
		if err := w.RemoveInstance(ctx, oldID); err != nil {
			s.log.Warn("failed to remove old instance by id", "gameserver", id, "error", err)
		}
	}
	if err := w.RemoveInstance(ctx, instanceName); err != nil {
		s.log.Debug("no stale instance to remove by name", "name", instanceName)
	}

	// Install phase — runs install-server in a short-lived instance, then marks installed
	if !gs.Installed {
		reportProgress(onProgress, model.PhaseInstalling, nil)

		s.rotateConsoleLogs(w, gs.VolumeName)
		s.copyDefaults(w, gs.VolumeName, defaultsDir)

		installName := naming.InstallInstanceName(id)
		w.RemoveInstance(ctx, installName)

		s.log.Info("running install phase", "gameserver", id)
		installID, installErr := w.CreateInstance(ctx, worker.InstanceOptions{
			Name:          installName,
			Image:         game.ResolveImage(map[string]string(gs.Env)),
			Env:           env,
			Ports:         ports,
			VolumeName:    gs.VolumeName,
			MemoryLimitMB: gs.MemoryLimitMB,
			CPULimit:      gs.CPULimit,
			CPUEnforced:   gs.CPUEnforced,
			Binds:         binds,
			Entrypoint:    []string{"/bin/sh", "-c", "/scripts/install-server"},
		})
		if installErr != nil {
			s.setError(id, userFriendlyError("Failed to create install instance", installErr))
			return fmt.Errorf("creating install instance for gameserver %s: %w", id, installErr)
		}

		if installErr = w.StartInstance(ctx, installID, ""); installErr != nil {
			w.RemoveInstance(ctx, installID)
			s.setError(id, userFriendlyError("Failed to start install instance", installErr))
			return fmt.Errorf("starting install instance for gameserver %s: %w", id, installErr)
		}

		exitCode, installErr := s.waitForInstanceExit(ctx, w, installID)

		// Copy install output to the volume's console log
		if logReader, logErr := w.InstanceLogs(ctx, installID, 0, false); logErr == nil {
			logData, _ := io.ReadAll(logReader)
			logReader.Close()
			if len(logData) > 0 {
				logPath := ".gamejanitor/logs/console.log"
				w.WriteFile(ctx, gs.VolumeName, logPath, logData, 0644)
			}
			if exitCode != 0 || installErr != nil {
				out := string(logData)
				if len(out) > 500 {
					out = out[len(out)-500:]
				}
				s.log.Error("install phase failed", "gameserver", id, "exit_code", exitCode, "output", out)
			}
		}

		w.RemoveInstance(ctx, installID)

		if installErr != nil {
			s.setError(id, "Install phase failed.")
			return fmt.Errorf("install phase for gameserver %s: %w", id, installErr)
		}
		if exitCode != 0 {
			s.setError(id, fmt.Sprintf("Install script failed with exit code %d.", exitCode))
			return fmt.Errorf("install-server exited with code %d for gameserver %s", exitCode, id)
		}

		gs.Installed = true
		if err := s.store.UpdateGameserver(gs); err != nil {
			return fmt.Errorf("marking gameserver %s as installed: %w", id, err)
		}
		s.log.Info("install phase complete", "gameserver", id)
	}

	// Start phase
	reportProgress(onProgress, model.PhaseStarting, nil)

	instanceID, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:          instanceName,
		Image:         game.ResolveImage(map[string]string(gs.Env)),
		Env:           env,
		Ports:         ports,
		VolumeName:    gs.VolumeName,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
		CPUEnforced:   gs.CPUEnforced,
		Binds:         binds,
		Entrypoint:    []string{"/bin/sh", "-c", "exec /scripts/start-server"},
	})
	if err != nil {
		s.setError(id, userFriendlyError("Failed to create instance", err))
		return fmt.Errorf("creating instance for gameserver %s: %w", id, err)
	}

	gs.InstanceID = &instanceID
	gs.DesiredState = "running"
	gs.AppliedConfig = gs.SnapshotConfig()
	if err := s.store.UpdateGameserver(gs); err != nil {
		w.RemoveInstance(ctx, instanceID)
		return err
	}
	s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceCreating, id, nil))

	if err := w.StartInstance(ctx, instanceID, game.ReadyPattern); err != nil {
		s.setError(id, userFriendlyError("Failed to start instance", err))
		return fmt.Errorf("starting instance for gameserver %s: %w", id, err)
	}

	if s.statusProvider != nil {
		s.statusProvider.SetRunning(id)
	}
	s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStarted, id, nil))

	s.log.Info("gameserver started", "gameserver", id, "instance_id", instanceID[:12])
	return nil
}

// Stop validates preconditions and stops a running gameserver. Blocks until
// the instance is stopped and removed.
func (s *Service) Stop(ctx context.Context, id string) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status == controller.StatusStopped || gs.Status == controller.StatusError {
		s.log.Info("gameserver already stopped, skipping stop", "gameserver", id, "status", gs.Status)
		return nil
	}

	// Clear runtime state BEFORE sending SIGTERM. The sandbox exit watcher fires
	// the "die" event almost immediately — if the runtime state still says "running",
	// the status manager treats it as an unexpected death.
	if s.statusProvider != nil {
		s.statusProvider.SetStopped(id)
	}
	s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, id, nil))

	return s.stopInstance(ctx, id)
}

// stopInstance performs the raw stop work without validation or status updates.
// Used by Stop, Restart, Archive, Migrate, UpdateServerGame, and Reinstall.
func (s *Service) stopInstance(ctx context.Context, id string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s for stop: %w", id, err)
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	if gs.InstanceID != nil {
		w := s.dispatcher.WorkerFor(id)
		if w == nil {
			s.log.Warn("worker unavailable during stop, skipping instance cleanup", "gameserver", id)
		} else {
			execCtx, execCancel := context.WithTimeout(ctx, 15*time.Second)
			_, _, _, execErr := w.Exec(execCtx, *gs.InstanceID, []string{"/scripts/stop-server"})
			execCancel()
			if execErr != nil {
				s.log.Info("stop-server script not available or failed, proceeding with instance stop", "gameserver", id, "error", execErr)
			}

			stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
			if err := w.StopInstance(stopCtx, *gs.InstanceID, 10); err != nil {
				s.log.Warn("failed to stop instance gracefully", "gameserver", id, "error", err)
			}
			stopCancel()
			if err := w.RemoveInstance(ctx, *gs.InstanceID); err != nil {
				s.log.Warn("failed to remove instance", "gameserver", id, "error", err)
			}
		}
	}

	gs, err = s.store.GetGameserver(id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found after stop", id)
	}
	gs.InstanceID = nil
	gs.DesiredState = "stopped"
	if err := s.store.UpdateGameserver(gs); err != nil {
		return err
	}

	// Clear any worker state that was re-injected by the exit event during stop.
	if s.statusProvider != nil {
		s.statusProvider.SetStopped(id)
	}

	s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopped, id, nil))
	s.log.Info("gameserver stopped", "gameserver", id)
	return nil
}

// Restart stops a running gameserver and starts it again. Blocks until complete.
func (s *Service) Restart(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	if gs.Status != controller.StatusStopped && gs.Status != controller.StatusError {
		if s.statusProvider != nil {
			s.statusProvider.SetStopped(id)
		}
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, id, nil))

		if err := s.stopInstance(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for restart: %w", err)
		}
	}

	return s.startInstance(ctx, id, onProgress)
}

// UpdateServerGame stops the gameserver, pulls the latest image, runs the
// update-server script, and restarts. Blocks until complete.
func (s *Service) UpdateServerGame(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	s.log.Info("updating game for gameserver", "gameserver", id, "game", game.ID)

	s.broadcaster.Publish(event.NewSystemEvent(event.EventImagePulling, id, nil))

	if gs.Status != controller.StatusStopped {
		if s.statusProvider != nil {
			s.statusProvider.SetStopped(id)
		}
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, id, nil))

		if err := s.stopInstance(ctx, id); err != nil {
			s.setError(id, controller.OperationFailedReason("Game update failed", err))
			return fmt.Errorf("stopping gameserver for update: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", fmt.Errorf("worker unavailable")))
		return fmt.Errorf("worker unavailable for gameserver %s", id)
	}

	gs, err = s.store.GetGameserver(id)
	if err != nil || gs == nil {
		return fmt.Errorf("re-reading gameserver %s after stop for update: %w", id, err)
	}

	// Pull latest image
	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env)), nil); err != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("pulling image for update: %w", err)
	}

	// Prepare scripts
	scriptDir, _, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("preparing scripts for update: %w", err)
	}
	updateBinds := []string{scriptDir + ":/scripts:ro"}

	env, err := mergeEnv(game, gs)
	if err != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("merging env for update: %w", err)
	}

	// Run update-server in a short-lived instance
	tempName := naming.UpdateInstanceName(id)
	tempID, err := w.CreateInstance(ctx, worker.InstanceOptions{
		Name:       tempName,
		Image:      game.ResolveImage(map[string]string(gs.Env)),
		Env:        env,
		VolumeName: gs.VolumeName,
		Binds:      updateBinds,
		Entrypoint: []string{"/bin/sh", "-c", "/scripts/update-server"},
	})
	if err != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("creating temp instance for update: %w", err)
	}
	defer w.RemoveInstance(ctx, tempID)

	if err := w.StartInstance(ctx, tempID, ""); err != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", err))
		return fmt.Errorf("starting temp instance for update: %w", err)
	}

	exitCode, waitErr := s.waitForInstanceExit(ctx, w, tempID)
	if waitErr != nil {
		s.setError(id, controller.OperationFailedReason("Game update failed", waitErr))
		return fmt.Errorf("waiting for update-server: %w", waitErr)
	}
	if exitCode != 0 {
		s.log.Error("update-server failed", "gameserver", id, "exit_code", exitCode)
		s.setError(id, controller.OperationFailedReason("Game update failed", fmt.Errorf("exit code %d", exitCode)))
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	s.log.Info("game updated, restarting gameserver", "gameserver", id)
	return s.startInstance(ctx, id, onProgress)
}

// Reinstall stops the gameserver, wipes the volume, and performs a fresh install.
// Blocks until complete.
func (s *Service) Reinstall(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
	gs, err := s.getGameserverWithStatus(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("reinstalling gameserver (full wipe)", "gameserver", id)

	s.broadcaster.Publish(event.NewSystemEvent(event.EventImagePulling, id, nil))

	if gs.Status != controller.StatusStopped {
		if s.statusProvider != nil {
			s.statusProvider.SetStopped(id)
		}
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopping, id, nil))

		if err := s.stopInstance(ctx, id); err != nil {
			s.setError(id, controller.OperationFailedReason("Reinstall failed", err))
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		s.setError(id, controller.OperationFailedReason("Reinstall failed", fmt.Errorf("worker unavailable")))
		return fmt.Errorf("worker unavailable for gameserver %s", id)
	}

	gs, err = s.store.GetGameserver(id)
	if err != nil || gs == nil {
		return fmt.Errorf("re-reading gameserver %s after stop for reinstall: %w", id, err)
	}

	gs.Installed = false
	if err := s.store.UpdateGameserver(gs); err != nil {
		s.setError(id, controller.OperationFailedReason("Reinstall failed", fmt.Errorf("clearing installed flag")))
		return fmt.Errorf("clearing installed flag for reinstall: %w", err)
	}

	// Wipe all data
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.setError(id, controller.OperationFailedReason("Reinstall failed", err))
		return fmt.Errorf("removing volume for reinstall: %w", err)
	}
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		s.setError(id, controller.OperationFailedReason("Reinstall failed", err))
		return fmt.Errorf("recreating volume for reinstall: %w", err)
	}

	s.log.Info("volume wiped, starting fresh install", "gameserver", id)
	return s.startInstance(ctx, id, onProgress)
}

func mergeEnv(game *games.Game, gs *model.Gameserver) ([]string, error) {
	env := make(map[string]string)
	systemKeys := make(map[string]bool)

	for _, d := range game.DefaultEnv {
		env[d.Key] = d.Default
		if d.System {
			systemKeys[d.Key] = true
		}
	}

	for k, v := range gs.Env {
		if !systemKeys[k] {
			env[k] = v
		}
	}

	ports := gs.Ports
	gamePortToHost := make(map[string]int)
	for _, gp := range game.DefaultPorts {
		for _, p := range ports {
			if p.Name == gp.Name {
				gamePortToHost[strconv.Itoa(gp.Port)] = int(p.HostPort)
				break
			}
		}
	}
	for _, d := range game.DefaultEnv {
		if !d.System {
			continue
		}
		if hp, ok := gamePortToHost[d.Default]; ok {
			env[d.Key] = strconv.Itoa(hp)
		}
	}

	if gs.MemoryLimitMB > 0 {
		env["MEMORY_LIMIT_MB"] = strconv.Itoa(gs.MemoryLimitMB)
	}

	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result, nil
}

func parseGameserverPorts(gs *model.Gameserver) ([]worker.PortBinding, error) {
	bindings := make([]worker.PortBinding, len(gs.Ports))
	for i, p := range gs.Ports {
		bindings[i] = worker.PortBinding{
			HostPort:     int(p.HostPort),
			InstancePort: int(p.InstancePort),
			Protocol:     p.Protocol,
		}
	}
	return bindings, nil
}

func (s *Service) rotateConsoleLogs(w worker.Worker, volumeName string) {
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

func (s *Service) copyDefaults(w worker.Worker, volumeName string, defaultsDir string) {
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

func (s *Service) waitForInstanceExit(ctx context.Context, w worker.Worker, instanceID string) (int, error) {
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

// reportProgress calls onProgress if non-nil.
func reportProgress(onProgress operation.ProgressFunc, phase model.OperationPhase, progress *model.OperationProgress) {
	if onProgress != nil {
		onProgress(phase, progress)
	}
}
