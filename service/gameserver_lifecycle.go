package service

import (
	"github.com/warsmite/gamejanitor/controller"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/pkg/naming"
	"github.com/warsmite/gamejanitor/model"
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

func (s *GameserverService) Start(ctx context.Context, id string) error {
	gs, err := model.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	switch gs.Status {
	case controller.StatusInstalling, controller.StatusStarting, controller.StatusStarted, controller.StatusRunning:
		s.log.Info("gameserver already active, skipping start", "id", id, "status", gs.Status)
		return nil
	}

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverStart,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: id,
		Gameserver:   gs,
	})

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return controller.ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, id)
	}

	w := s.dispatcher.WorkerFor(id)

	// Pull image
	s.broadcaster.Publish(ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	if err := w.PullImage(ctx, game.BaseImage); err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: "Failed to pull game image. Check your internet connection.", Timestamp: time.Now()})
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}
	s.broadcaster.Publish(ImagePulledEvent{GameserverID: id, Timestamp: time.Now()})

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: "Failed to configure environment variables.", Timestamp: time.Now()})
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	if gs.Installed {
		env = append(env, controller.EnvSkipInstall)
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: "Invalid port configuration.", Timestamp: time.Now()})
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Prepare game scripts on the target worker (extracts locally for bind-mounting)
	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: "Failed to extract game scripts.", Timestamp: time.Now()})
		return fmt.Errorf("preparing scripts for gameserver %s: %w", id, err)
	}

	binds := []string{
		scriptDir + ":/scripts:ro",
	}
	if defaultsDir != "" {
		binds = append(binds, defaultsDir+":/defaults:ro")
	}

	// Remove old container if exists (stale from prior run/crash).
	// Clear ContainerID first so late Docker "die" events from the old container
	// are recognized as stale by the StatusManager.
	containerName := naming.ContainerName(id)
	if gs.ContainerID != nil {
		oldID := *gs.ContainerID
		gs.ContainerID = nil
		if err := model.UpdateGameserver(s.db, gs); err != nil {
			s.log.Warn("failed to clear old container ID", "id", id, "error", err)
		}
		if err := w.RemoveContainer(ctx, oldID); err != nil {
			s.log.Warn("failed to remove old container by id", "id", id, "error", err)
		}
	}
	if err := w.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no stale container to remove by name", "name", containerName)
	}

	// Create container
	containerID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:          containerName,
		Image:         game.BaseImage,
		Env:           env,
		Ports:         ports,
		VolumeName:    gs.VolumeName,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
		CPUEnforced:   gs.CPUEnforced,
		Binds:         binds,
	})
	if err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: userFriendlyError("Failed to create container", err), Timestamp: time.Now()})
		return fmt.Errorf("creating container for gameserver %s: %w", id, err)
	}

	// Save container ID (direct DB write — must persist immediately)
	gs.ContainerID = &containerID
	if err := model.UpdateGameserver(s.db, gs); err != nil {
		w.RemoveContainer(ctx, containerID)
		return err
	}
	s.broadcaster.Publish(ContainerCreatingEvent{GameserverID: id, Timestamp: time.Now()})

	// Start container
	if err := w.StartContainer(ctx, containerID); err != nil {
		s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: userFriendlyError("Failed to start container", err), Timestamp: time.Now()})
		return fmt.Errorf("starting container for gameserver %s: %w", id, err)
	}

	s.broadcaster.Publish(ContainerStartedEvent{GameserverID: id, Timestamp: time.Now()})

	if s.readyWatcher != nil {
		s.readyWatcher.Watch(id, w, containerID)
	}

	s.log.Info("gameserver started", "id", id, "container_id", containerID[:12])
	return nil
}

func (s *GameserverService) Stop(ctx context.Context, id string) error {
	gs, err := model.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status == controller.StatusStopped {
		s.log.Info("gameserver already stopped, skipping", "id", id)
		return nil
	}

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverStop,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: id,
		Gameserver:   gs,
	})

	s.broadcaster.Publish(ContainerStoppingEvent{GameserverID: id, Timestamp: time.Now()})

	if gs.ContainerID != nil {
		w := s.dispatcher.WorkerFor(id)
		if err := w.StopContainer(ctx, *gs.ContainerID, 10); err != nil {
			s.log.Warn("failed to stop container gracefully", "id", id, "error", err)
		}
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container", "id", id, "error", err)
		}
	}

	// Clear container ID (direct DB write)
	gs, err = model.GetGameserver(s.db, id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found after stop", id)
	}
	gs.ContainerID = nil
	if err := model.UpdateGameserver(s.db, gs); err != nil {
		return err
	}

	s.broadcaster.Publish(ContainerStoppedEvent{GameserverID: id, Timestamp: time.Now()})
	s.log.Info("gameserver stopped", "id", id)
	return nil
}

func (s *GameserverService) Restart(ctx context.Context, id string) error {
	gs, err := model.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverRestart,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: id,
		Gameserver:   gs,
	})

	if gs.Status != controller.StatusStopped && gs.Status != controller.StatusError {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for restart: %w", err)
		}
	}

	return s.Start(ctx, id)
}

func (s *GameserverService) UpdateServerGame(ctx context.Context, id string) (err error) {
	gs, err := model.GetGameserver(s.db, id)
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

	s.log.Info("updating game for gameserver", "id", id, "game", game.ID)

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverUpdateGame,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: id,
		Gameserver:   gs,
	})

	s.broadcaster.Publish(ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	defer func() {
		if err != nil {
			s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: operationFailedReason("Game update failed", err), Timestamp: time.Now()})
		}
	}()

	if gs.Status != controller.StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for update: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	// Pull latest image
	if err := w.PullImage(ctx, game.BaseImage); err != nil {
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
		Image:      game.BaseImage,
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
		s.log.Error("update-server failed", "id", id, "exit_code", exitCode, "stdout", stdout, "stderr", stderr)
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	if err := w.StopContainer(ctx, tempID, 10); err != nil {
		s.log.Warn("failed to stop temp update container", "id", id, "error", err)
	}

	s.log.Info("game updated, restarting gameserver", "id", id)
	return s.Start(ctx, id)
}

func (s *GameserverService) Reinstall(ctx context.Context, id string) (err error) {
	gs, err := model.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("reinstalling gameserver (full wipe)", "id", id)

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverReinstall,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: id,
		Gameserver:   gs,
	})

	s.broadcaster.Publish(ImagePullingEvent{GameserverID: id, Timestamp: time.Now()})
	defer func() {
		if err != nil {
			s.broadcaster.Publish(GameserverErrorEvent{GameserverID: id, Reason: operationFailedReason("Reinstall failed", err), Timestamp: time.Now()})
		}
	}()

	if gs.Status != controller.StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	gs.Installed = false
	if err := model.UpdateGameserver(s.db, gs); err != nil {
		return fmt.Errorf("clearing installed flag for reinstall: %w", err)
	}

	// Wipe all data by removing and recreating the volume
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("removing volume for reinstall: %w", err)
	}
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("recreating volume for reinstall: %w", err)
	}

	s.log.Info("volume wiped, starting fresh install", "id", id)
	return s.Start(ctx, id)
}

// Port mapping from gameserver's ports JSON
// flexInt handles JSON values that may be a number or a string containing a number.
type flexInt int

func (fi *flexInt) UnmarshalJSON(b []byte) error {
	// Try number first
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*fi = flexInt(n)
		return nil
	}
	// Try string
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("cannot unmarshal %s into int or string", string(b))
	}
	if s == "" {
		*fi = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("cannot parse %q as int: %w", s, err)
	}
	*fi = flexInt(n)
	return nil
}

type portMapping struct {
	Name          string  `json:"name"`
	HostPort      flexInt `json:"host_port"`
	ContainerPort flexInt `json:"container_port"`
	Protocol      string  `json:"protocol"`
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
	var overrides map[string]string
	if err := json.Unmarshal(gs.Env, &overrides); err != nil {
		return nil, fmt.Errorf("parsing gameserver env: %w", err)
	}
	for k, v := range overrides {
		if !systemKeys[k] {
			env[k] = v
		}
	}

	// Step 3: Override system fields from port mappings
	var ports []portMapping
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return nil, fmt.Errorf("parsing gameserver ports: %w", err)
	}

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
	var ports []portMapping
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return nil, fmt.Errorf("parsing gameserver ports: %w", err)
	}

	bindings := make([]worker.PortBinding, len(ports))
	for i, p := range ports {
		bindings[i] = worker.PortBinding{
			HostPort:      int(p.HostPort),
			ContainerPort: int(p.ContainerPort),
			Protocol:      p.Protocol,
		}
	}
	return bindings, nil
}
