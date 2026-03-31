package gameserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/worker"
)

// userFriendlyError translates Docker errors into messages a user can act on.
func userFriendlyError(prefix string, err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated") {
		return "Port conflict: a port is already in use. Edit ports or stop the conflicting gameserver."
	}
	return prefix + "."
}

// operationFailedReason builds a user-facing error reason for failed multi-step
// operations (update, reinstall, migrate, restore).
func operationFailedReason(prefix string, err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "pulling image") || strings.Contains(msg, "pull"):
		return prefix + ". Check your internet connection."
	case strings.Contains(msg, "volume") || strings.Contains(msg, "disk") || strings.Contains(msg, "no space"):
		return prefix + ". There may be a storage issue."
	default:
		return prefix + "."
	}
}

func (s *GameserverService) Start(ctx context.Context, id string) (err error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	switch gs.Status {
	case controller.StatusInstalling, controller.StatusStarting, controller.StatusStarted, controller.StatusRunning:
		s.log.Info("gameserver already active, skipping start", "gameserver", id, "status", gs.Status)
		return nil
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
		limitErr := s.checkWorkerLimitsExcluding(*gs.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB), gs.ID)
		if limitErr != nil {
			s.log.Warn("assigned node cannot fit gameserver resources, attempting auto-migration",
				"gameserver", id, "node_id", *gs.NodeID, "error", limitErr)

			candidates := s.dispatcher.RankWorkersForPlacement(gs.NodeTags)
			foundNode := ""
			for _, c := range candidates {
				if c.NodeID == *gs.NodeID {
					continue
				}
				if err := s.checkWorkerLimits(c.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err == nil {
					foundNode = c.NodeID
					break
				}
			}

			if foundNode == "" {
				return fmt.Errorf("cannot start: node %s lacks capacity and no other node can fit %d MB / %.1f CPU", *gs.NodeID, gs.MemoryLimitMB, gs.CPULimit)
			}

			s.log.Info("auto-migrating before start", "gameserver", id, "from_node", *gs.NodeID, "to_node", foundNode)
			if err := s.MigrateGameserver(ctx, id, foundNode); err != nil {
				return fmt.Errorf("auto-migration before start failed: %w", err)
			}

			// Reload gameserver after migration (node_id changed)
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

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, opErr := s.trackActivity(ctx, id, workerID, model.OpStart, nil, nil)
	if opErr != nil {
		return opErr
	}
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
		defer func() {
			if err != nil {
				s.failActivity(id, err)
			} else {
				s.completeActivity(id)
			}
		}()
	}

	// Download game files via Steam depot downloader for all Steam games.
	// Anonymous games need no credentials. Auth-required games need a linked Steam account.
	var depotDir string
	depotAppID := game.DepotAppID()
	if depotAppID != 0 {
		accountName := ""
		refreshToken := ""

		if game.SteamLogin.RequiresAuth() {
			accountName = s.settingsSvc.GetString(settings.SettingSteamAccountName)
			refreshToken = s.settingsSvc.GetString(settings.SettingSteamRefreshToken)
			if refreshToken == "" {
				s.broadcaster.Publish(controller.GameserverErrorEvent{
					GameserverID: id,
					Reason:       "This game requires a linked Steam account. Run 'gamejanitor steam login' to configure.",
					Timestamp:    time.Now(),
				})
				return fmt.Errorf("game %s requires Steam auth but no credentials configured", game.ID)
			}
		}

		s.broadcaster.Publish(controller.DepotDownloadingEvent{
			GameserverID: id,
			AppID:        depotAppID,
			Timestamp:    time.Now(),
		})
		if s.operations != nil {
			s.operations.SetOperation(id, "start", model.PhaseDownloadingGame)
		}

		depotResult, depotErr := w.EnsureDepot(ctx, depotAppID, "public", accountName, refreshToken, func(p worker.DepotProgress) {
			if s.operations != nil && p.TotalBytes > 0 {
				s.operations.UpdateProgress(id, model.OperationProgress{
					Percent:        float64(p.CompletedBytes) / float64(p.TotalBytes) * 100,
					CompletedBytes: p.CompletedBytes,
					TotalBytes:     p.TotalBytes,
				})
			}
		})
		if depotErr != nil {
			s.broadcaster.Publish(controller.GameserverErrorEvent{
				GameserverID: id,
				Reason:       "Failed to download game files from Steam.",
				Timestamp:    time.Now(),
			})
			return fmt.Errorf("depot download for gameserver %s: %w", id, depotErr)
		}

		depotDir = depotResult.DepotDir

		if depotResult.Cached {
			s.broadcaster.Publish(controller.DepotCachedEvent{
				GameserverID: id,
				AppID:        depotAppID,
				Timestamp:    time.Now(),
			})
		} else {
			s.broadcaster.Publish(controller.DepotCompleteEvent{
				GameserverID: id,
				AppID:           depotAppID,
				BytesDownloaded: depotResult.BytesDownloaded,
				Timestamp:       time.Now(),
			})
		}
	}

	// Pull image
	if s.operations != nil {
		s.operations.SetOperation(id, "start", model.PhasePullingImage)
	}
	s.broadcaster.Publish(controller.ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env))); err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: "Failed to pull game image. Check your internet connection.", Timestamp: time.Now()})
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}
	s.broadcaster.Publish(controller.ImagePulledEvent{GameserverID: id, Timestamp: time.Now()})

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: "Failed to configure environment variables.", Timestamp: time.Now()})
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	if gs.Installed {
		env = append(env, controller.EnvSkipInstall)
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: "Invalid port configuration.", Timestamp: time.Now()})
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Prepare game scripts on the target worker (extracts locally for bind-mounting)
	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: "Failed to extract game scripts.", Timestamp: time.Now()})
		return fmt.Errorf("preparing scripts for gameserver %s: %w", id, err)
	}

	// Copy depot files into the volume on the host (outside the container).
	// Doing this inside the container hits the cgroup memory limit because
	// the kernel page cache from copying large depots (3+ GB) counts against it.
	if depotDir != "" && !gs.Installed {
		s.log.Info("copying depot to volume", "gameserver", id, "depot", depotDir)
		if err := w.CopyDepotToVolume(ctx, depotDir, gs.VolumeName); err != nil {
			s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: "Failed to copy game files to volume.", Timestamp: time.Now()})
			return fmt.Errorf("copying depot to volume for gameserver %s: %w", id, err)
		}
	}

	// Reconcile mods — ensure DB-tracked mods exist on the volume before starting.
	// Non-fatal: missing mods are logged but don't block the start.
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

	// Remove old container if exists (stale from prior run/crash).
	// Clear ContainerID first so late Docker "die" events from the old container
	// are recognized as stale by the StatusManager.
	containerName := naming.ContainerName(id)
	if gs.ContainerID != nil {
		oldID := *gs.ContainerID
		gs.ContainerID = nil
		if err := s.store.UpdateGameserver(gs); err != nil {
			s.log.Warn("failed to clear old container ID", "gameserver", id, "error", err)
		}
		if err := w.RemoveContainer(ctx, oldID); err != nil {
			s.log.Warn("failed to remove old container by id", "gameserver", id, "error", err)
		}
	}
	if err := w.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no stale container to remove by name", "name", containerName)
	}

	// Create container — the install script runs when the container starts
	if s.operations != nil {
		s.operations.SetOperation(id, "start", model.PhaseInstalling)
	}
	containerID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:          containerName,
		Image:         game.ResolveImage(map[string]string(gs.Env)),
		Env:           env,
		Ports:         ports,
		VolumeName:    gs.VolumeName,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
		CPUEnforced:   gs.CPUEnforced,
		Binds:         binds,
	})
	if err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: userFriendlyError("Failed to create container", err), Timestamp: time.Now()})
		return fmt.Errorf("creating container for gameserver %s: %w", id, err)
	}

	// Save container ID and snapshot the applied config for restart-required detection
	gs.ContainerID = &containerID
	gs.AppliedConfig = gs.SnapshotConfig()
	if err := s.store.UpdateGameserver(gs); err != nil {
		w.RemoveContainer(ctx, containerID)
		return err
	}
	s.broadcaster.Publish(controller.ContainerCreatingEvent{GameserverID: id, Timestamp: time.Now()})

	// Start container
	if err := w.StartContainer(ctx, containerID); err != nil {
		s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: userFriendlyError("Failed to start container", err), Timestamp: time.Now()})
		return fmt.Errorf("starting container for gameserver %s: %w", id, err)
	}

	s.broadcaster.Publish(controller.ContainerStartedEvent{GameserverID: id, Timestamp: time.Now()})

	if s.operations != nil {
		s.operations.SetOperation(id, "start", model.PhaseStarting)
	}

	if s.readyWatcher != nil {
		s.readyWatcher.Watch(id, w, containerID)
	}

	s.log.Info("gameserver started", "gameserver", id, "container_id", containerID[:12])
	return nil
}

func (s *GameserverService) Stop(ctx context.Context, id string) (err error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	if s.readyWatcher != nil {
		s.readyWatcher.Stop(id)
	}

	if gs.Status == controller.StatusStopped {
		s.log.Info("gameserver already stopped, skipping", "gameserver", id)
		return nil
	}

	s.broadcaster.Publish(controller.ContainerStoppingEvent{GameserverID: id, Timestamp: time.Now()})

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, _ := s.trackActivity(ctx, id, workerID, model.OpStop, nil, nil)
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
		defer func() {
			if err != nil {
				s.failActivity(id, err)
			} else {
				s.completeActivity(id)
			}
		}()
	}

	if gs.ContainerID != nil {
		w := s.dispatcher.WorkerFor(id)
		if w == nil {
			s.log.Warn("worker unavailable during stop, skipping container cleanup", "gameserver", id)
		} else {
			// Run stop-server script if it exists — announces shutdown, saves world
			_, _, _, execErr := w.Exec(ctx, *gs.ContainerID, []string{"/scripts/stop-server"})
			if execErr != nil {
				s.log.Debug("stop-server script not available or failed, proceeding with container stop", "gameserver", id, "error", execErr)
			}
			if err := w.StopContainer(ctx, *gs.ContainerID, 10); err != nil {
				s.log.Warn("failed to stop container gracefully", "gameserver", id, "error", err)
			}
			if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
				s.log.Warn("failed to remove container", "gameserver", id, "error", err)
			}
		}
	}

	// Clear container ID (direct DB write)
	gs, err = s.store.GetGameserver(id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found after stop", id)
	}
	gs.ContainerID = nil
	if err := s.store.UpdateGameserver(gs); err != nil {
		return err
	}

	s.broadcaster.Publish(controller.ContainerStoppedEvent{GameserverID: id, Timestamp: time.Now()})
	s.log.Info("gameserver stopped", "gameserver", id)
	return nil
}

func (s *GameserverService) Restart(ctx context.Context, id string) (err error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, opErr := s.trackActivity(ctx, id, workerID, model.OpRestart, nil, nil)
	if opErr != nil {
		return opErr
	}
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
		defer func() {
			if err != nil {
				s.failActivity(id, err)
			} else {
				s.completeActivity(id)
			}
		}()
	}

	if gs.Status != controller.StatusStopped && gs.Status != controller.StatusError {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for restart: %w", err)
		}
	}

	return s.Start(ctx, id)
}

func (s *GameserverService) UpdateServerGame(ctx context.Context, id string) (err error) {
	gs, err := s.store.GetGameserver(id)
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

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, opErr := s.trackActivity(ctx, id, workerID, model.OpUpdate, nil, nil)
	if opErr != nil {
		return opErr
	}
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
	}

	s.broadcaster.Publish(controller.ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	defer func() {
		if err != nil {
			s.failActivity(id, err)
			s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: operationFailedReason("Game update failed", err), Timestamp: time.Now()})
		} else {
			s.completeActivity(id)
		}
	}()

	if gs.Status != controller.StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for update: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	// Pull latest image
	if err := w.PullImage(ctx, game.ResolveImage(map[string]string(gs.Env))); err != nil {
		return fmt.Errorf("pulling image for update: %w", err)
	}

	// Prepare scripts on the target worker for update container
	scriptDir, _, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		return fmt.Errorf("preparing scripts for update: %w", err)
	}
	updateBinds := []string{scriptDir + ":/scripts:ro"}

	// Merge env vars so the update script has access to config (VERSION, EULA, etc.)
	env, err := mergeEnv(game, gs)
	if err != nil {
		return fmt.Errorf("merging env for update: %w", err)
	}

	// Run update-server in temp container
	tempName := naming.UpdateContainerName(id)
	tempID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       tempName,
		Image:      game.ResolveImage(map[string]string(gs.Env)),
		Env:        env,
		VolumeName: gs.VolumeName,
		Binds:      updateBinds,
	})
	if err != nil {
		return fmt.Errorf("creating temp container for update: %w", err)
	}
	defer w.RemoveContainer(ctx, tempID)

	if err := w.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for update: %w", err)
	}

	exitCode, stdout, stderr, err := w.Exec(ctx, tempID, []string{"/scripts/update-server"})
	if err != nil {
		return fmt.Errorf("running update-server: %w", err)
	}
	if exitCode != 0 {
		s.log.Error("update-server failed", "gameserver", id, "exit_code", exitCode, "stdout", stdout, "stderr", stderr)
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	if err := w.StopContainer(ctx, tempID, 10); err != nil {
		s.log.Warn("failed to stop temp update container", "gameserver", id, "error", err)
	}

	s.log.Info("game updated, restarting gameserver", "gameserver", id)
	return s.Start(ctx, id)
}

func (s *GameserverService) Reinstall(ctx context.Context, id string) (err error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("reinstalling gameserver (full wipe)", "gameserver", id)

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	opID, opErr := s.trackActivity(ctx, id, workerID, model.OpReinstall, nil, nil)
	if opErr != nil {
		return opErr
	}
	if opID != "" {
		ctx = WithActivityID(ctx, opID)
	}


	s.broadcaster.Publish(controller.ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	defer func() {
		if err != nil {
			s.failActivity(id, err)
			s.broadcaster.Publish(controller.GameserverErrorEvent{GameserverID: id, Reason: operationFailedReason("Reinstall failed", err), Timestamp: time.Now()})
		} else {
			s.completeActivity(id)
		}
	}()

	if gs.Status != controller.StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	gs.Installed = false
	if err := s.store.UpdateGameserver(gs); err != nil {
		return fmt.Errorf("clearing installed flag for reinstall: %w", err)
	}

	// Wipe all data by removing and recreating the volume
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("removing volume for reinstall: %w", err)
	}
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("recreating volume for reinstall: %w", err)
	}

	s.log.Info("volume wiped, starting fresh install", "gameserver", id)
	return s.Start(ctx, id)
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

	// Step 2: Merge gameserver env overrides (user values win)
	for k, v := range gs.Env {
		if !systemKeys[k] {
			env[k] = v
		}
	}

	// Step 3: Override system fields from port mappings
	ports := gs.Ports

	// Map game definition ports to their allocated host ports
	// System env vars whose default matches a game port get overridden with the host port
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

	// Step 4: Inject MEMORY_LIMIT_MB
	if gs.MemoryLimitMB > 0 {
		env["MEMORY_LIMIT_MB"] = strconv.Itoa(gs.MemoryLimitMB)
	}

	// Step 5: Convert to []string
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
			HostPort:      int(p.HostPort),
			ContainerPort: int(p.ContainerPort),
			Protocol:      p.Protocol,
		}
	}
	return bindings, nil
}
