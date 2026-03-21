package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
)

// userFriendlyError translates Docker errors into messages a user can act on.
func userFriendlyError(prefix string, err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated") {
		return "Port conflict: a port is already in use. Edit ports or stop the conflicting gameserver."
	}
	return prefix + "."
}

func (s *GameserverService) Start(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	switch gs.Status {
	case StatusPulling, StatusStarting, StatusStarted, StatusRunning:
		s.log.Info("gameserver already active, skipping start", "id", id, "status", gs.Status)
		return nil
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, id)
	}

	w := s.dispatcher.WorkerFor(id)

	// Pull image
	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusPulling, ""); err != nil {
		return err
	}
	if err := w.PullImage(ctx, game.BaseImage); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to pull game image. Check your internet connection.")
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to configure environment variables.")
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	if gs.Installed {
		env = append(env, EnvSkipInstall)
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Invalid port configuration.")
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Prepare game scripts on the target worker (extracts locally for bind-mounting)
	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to extract game scripts.")
		return fmt.Errorf("preparing scripts for gameserver %s: %w", id, err)
	}

	binds := []string{
		scriptDir + ":/scripts:ro",
	}
	if defaultsDir != "" {
		binds = append(binds, defaultsDir+":/defaults:ro")
	}

	// Remove old container if exists (stale from prior run/crash).
	// Always try by name in case the DB lost track of the container ID
	// (e.g. Stop cleared ContainerID but RemoveContainer failed).
	containerName := docker.ContainerPrefix + id
	if gs.ContainerID != nil {
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
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
		Binds:         binds,
	})
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, userFriendlyError("Failed to create container", err))
		return fmt.Errorf("creating container for gameserver %s: %w", id, err)
	}

	// Save container ID
	gs.ContainerID = &containerID
	gs.Status = StatusStarting
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		w.RemoveContainer(ctx, containerID)
		return err
	}

	// Start container
	if err := w.StartContainer(ctx, containerID); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, userFriendlyError("Failed to start container", err))
		return fmt.Errorf("starting container for gameserver %s: %w", id, err)
	}

	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStarted, ""); err != nil {
		return err
	}

	if s.readyWatcher != nil {
		s.readyWatcher.Watch(id, w, containerID)
	}

	s.log.Info("gameserver started", "id", id, "container_id", containerID[:12])
	return nil
}

func (s *GameserverService) Stop(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status == StatusStopped {
		s.log.Info("gameserver already stopped, skipping", "id", id)
		return nil
	}

	// Don't overwrite operation statuses (updating, reinstalling, etc.)
	// so the UI keeps showing the real operation in progress
	if !isOperationStatus(gs.Status) {
		if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStopping, ""); err != nil {
			return err
		}
	}

	if gs.ContainerID != nil {
		w := s.dispatcher.WorkerFor(id)
		if err := w.StopContainer(ctx, *gs.ContainerID, 10); err != nil {
			s.log.Warn("failed to stop container gracefully", "id", id, "error", err)
		}
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container", "id", id, "error", err)
		}
	}

	// Re-read from DB to avoid overwriting changes made by status manager during stop
	gs, err = models.GetGameserver(s.db, id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found after stop", id)
	}

	// Preserve operation status — the calling operation will transition
	// to the next status (e.g. Start() sets pulling)
	if isOperationStatus(gs.Status) {
		gs.ContainerID = nil
		if err := models.UpdateGameserver(s.db, gs); err != nil {
			return err
		}
		s.log.Info("gameserver container stopped (operation in progress)", "id", id, "status", gs.Status)
		return nil
	}

	oldStatus := gs.Status
	gs.ContainerID = nil
	gs.Status = StatusStopped
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		return err
	}

	s.broadcaster.Publish(StatusEvent{GameserverID: id, OldStatus: oldStatus, NewStatus: StatusStopped})
	s.log.Info("gameserver stopped", "id", id)
	return nil
}

func (s *GameserverService) Restart(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status != StatusStopped && gs.Status != StatusError {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for restart: %w", err)
		}
	}

	return s.Start(ctx, id)
}

func (s *GameserverService) UpdateServerGame(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found", gs.GameID)
	}

	s.log.Info("updating game for gameserver", "id", id, "game", game.ID)

	setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusUpdating, "")

	if gs.Status != StatusStopped {
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

	// Run update-server in temp container
	tempName := docker.UpdateContainerPrefix + id
	tempID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       tempName,
		Image:      game.BaseImage,
		Env:        []string{},
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

func (s *GameserverService) Reinstall(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("reinstalling gameserver (full wipe)", "id", id)

	setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusReinstalling, "")

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	gs.Installed = false
	if err := models.UpdateGameserver(s.db, gs); err != nil {
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

func mergeEnv(game *games.Game, gs *models.Gameserver) ([]string, error) {
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

func parseGameserverPorts(gs *models.Gameserver) ([]worker.PortBinding, error) {
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
