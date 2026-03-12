package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/google/uuid"
)

type GameserverService struct {
	db          *sql.DB
	docker      *docker.Client
	log         *slog.Logger
	broadcaster *EventBroadcaster
}

func NewGameserverService(db *sql.DB, dockerClient *docker.Client, broadcaster *EventBroadcaster, log *slog.Logger) *GameserverService {
	return &GameserverService{db: db, docker: dockerClient, broadcaster: broadcaster, log: log}
}

func (s *GameserverService) ListGameservers(filter models.GameserverFilter) ([]models.Gameserver, error) {
	return models.ListGameservers(s.db, filter)
}

func (s *GameserverService) GetGameserver(id string) (*models.Gameserver, error) {
	return models.GetGameserver(s.db, id)
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *models.Gameserver) error {
	gs.ID = uuid.New().String()
	gs.VolumeName = "gamejanitor-" + gs.ID
	gs.Status = StatusStopped

	s.log.Info("creating gameserver", "id", gs.ID, "name", gs.Name, "game_id", gs.GameID)

	if err := s.docker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("creating volume for gameserver %s: %w", gs.ID, err)
	}

	if err := models.CreateGameserver(s.db, gs); err != nil {
		s.docker.RemoveVolume(ctx, gs.VolumeName)
		return err
	}

	return nil
}

func (s *GameserverService) UpdateGameserver(gs *models.Gameserver) error {
	existing, err := models.GetGameserver(s.db, gs.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return fmt.Errorf("gameserver %s not found", gs.ID)
	}
	if existing.Status != StatusStopped {
		return fmt.Errorf("gameserver %s must be stopped before updating (current status: %s)", gs.ID, existing.Status)
	}

	s.log.Info("updating gameserver", "id", gs.ID)
	return models.UpdateGameserver(s.db, gs)
}

func (s *GameserverService) DeleteGameserver(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	s.log.Info("deleting gameserver", "id", id, "name", gs.Name)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver before delete: %w", err)
		}
	}

	if gs.ContainerID != nil {
		if err := s.docker.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container during delete", "id", id, "error", err)
		}
	}

	if err := s.docker.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove volume during delete", "id", id, "volume", gs.VolumeName, "error", err)
	}

	return models.DeleteGameserver(s.db, id)
}

func (s *GameserverService) Start(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	switch gs.Status {
	case StatusPulling, StatusStarting, StatusStarted, StatusRunning:
		s.log.Info("gameserver already active, skipping start", "id", id, "status", gs.Status)
		return nil
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return fmt.Errorf("getting game for gameserver %s: %w", id, err)
	}
	if game == nil {
		return fmt.Errorf("game %s not found for gameserver %s", gs.GameID, id)
	}

	// Pull image
	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusPulling); err != nil {
		return err
	}
	if err := s.docker.PullImage(ctx, game.Image); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError)
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError)
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError)
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Remove old container if exists (stale from prior run/crash)
	if gs.ContainerID != nil {
		if err := s.docker.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove old container", "id", id, "error", err)
		}
	}

	// Create container
	containerName := "gamejanitor-" + id
	containerID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:          containerName,
		Image:         game.Image,
		Env:           env,
		Ports:         ports,
		VolumeName:    gs.VolumeName,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
	})
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError)
		return fmt.Errorf("creating container for gameserver %s: %w", id, err)
	}

	// Save container ID
	gs.ContainerID = &containerID
	gs.Status = StatusStarting
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		s.docker.RemoveContainer(ctx, containerID)
		return err
	}

	// Start container
	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError)
		return fmt.Errorf("starting container for gameserver %s: %w", id, err)
	}

	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStarted); err != nil {
		return err
	}

	// GSQ stub: promote to running immediately (real GSQ polling in Phase 11)
	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusRunning); err != nil {
		return err
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
		return fmt.Errorf("gameserver %s not found", id)
	}

	if gs.Status == StatusStopped {
		s.log.Info("gameserver already stopped, skipping", "id", id)
		return nil
	}

	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStopping); err != nil {
		return err
	}

	if gs.ContainerID != nil {
		if err := s.docker.StopContainer(ctx, *gs.ContainerID, 30); err != nil {
			s.log.Warn("failed to stop container gracefully", "id", id, "error", err)
		}
		if err := s.docker.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container", "id", id, "error", err)
		}
	}

	// Clear container ID
	gs.ContainerID = nil
	gs.Status = StatusStopped
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		return err
	}

	s.log.Info("gameserver stopped", "id", id)
	return nil
}

func (s *GameserverService) Restart(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	if gs.Status != StatusStopped {
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
		return fmt.Errorf("gameserver %s not found", id)
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return fmt.Errorf("getting game for gameserver %s: %w", id, err)
	}
	if game == nil {
		return fmt.Errorf("game %s not found", gs.GameID)
	}

	s.log.Info("updating game for gameserver", "id", id, "game", game.ID)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for update: %w", err)
		}
	}

	// Pull latest image
	if err := s.docker.PullImage(ctx, game.Image); err != nil {
		return fmt.Errorf("pulling image for update: %w", err)
	}

	// Run update-server in temp container
	tempName := "gamejanitor-update-" + id
	tempID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       tempName,
		Image:      game.Image,
		Env:        []string{},
		VolumeName: gs.VolumeName,
	})
	if err != nil {
		return fmt.Errorf("creating temp container for update: %w", err)
	}
	defer s.docker.RemoveContainer(ctx, tempID)

	if err := s.docker.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for update: %w", err)
	}

	exitCode, stdout, stderr, err := s.docker.Exec(ctx, tempID, []string{"/scripts/update-server"})
	if err != nil {
		return fmt.Errorf("running update-server: %w", err)
	}
	if exitCode != 0 {
		s.log.Error("update-server failed", "id", id, "exit_code", exitCode, "stdout", stdout, "stderr", stderr)
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	if err := s.docker.StopContainer(ctx, tempID, 10); err != nil {
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
		return fmt.Errorf("gameserver %s not found", id)
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return fmt.Errorf("getting game for gameserver %s: %w", id, err)
	}
	if game == nil {
		return fmt.Errorf("game %s not found", gs.GameID)
	}

	s.log.Info("reinstalling gameserver", "id", id)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	// Remove .installed marker via temp container
	tempName := "gamejanitor-reinstall-" + id
	tempID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       tempName,
		Image:      game.Image,
		Env:        []string{},
		VolumeName: gs.VolumeName,
	})
	if err != nil {
		return fmt.Errorf("creating temp container for reinstall: %w", err)
	}
	defer s.docker.RemoveContainer(ctx, tempID)

	if err := s.docker.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for reinstall: %w", err)
	}

	exitCode, _, stderr, err := s.docker.Exec(ctx, tempID, []string{"rm", "-f", "/data/.installed"})
	if err != nil {
		return fmt.Errorf("removing install marker: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("removing install marker failed (exit %d): %s", exitCode, stderr)
	}

	if err := s.docker.StopContainer(ctx, tempID, 10); err != nil {
		s.log.Warn("failed to stop temp reinstall container", "id", id, "error", err)
	}

	s.log.Info("install marker removed, restarting gameserver", "id", id)
	return s.Start(ctx, id)
}


// Env var definition from game's default_env JSON
type envVarDef struct {
	Key     string   `json:"key"`
	Default string   `json:"default"`
	Label   string   `json:"label,omitempty"`
	Type    string   `json:"type,omitempty"`
	Options []string `json:"options,omitempty"`
	System  bool     `json:"system,omitempty"`
}

// Port mapping from gameserver's ports JSON
type portMapping struct {
	Name          string `json:"name"`
	HostPort      int    `json:"host_port"`
	ContainerPort int    `json:"container_port"`
	Protocol      string `json:"protocol"`
}

func mergeEnv(game *models.Game, gs *models.Gameserver) ([]string, error) {
	// Step 1: Parse game default_env → extract key/default pairs
	var defs []envVarDef
	if err := json.Unmarshal(game.DefaultEnv, &defs); err != nil {
		return nil, fmt.Errorf("parsing game default_env: %w", err)
	}

	env := make(map[string]string)
	systemKeys := make(map[string]bool)

	for _, d := range defs {
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

	// Build port name → container_port map for system env var overrides
	// System env keys that end with _PORT get their value from the matching container port
	for _, d := range defs {
		if !d.System {
			continue
		}
		// Find matching port: system env var default value matches a container port
		for _, p := range ports {
			if d.Default == strconv.Itoa(p.ContainerPort) {
				env[d.Key] = strconv.Itoa(p.ContainerPort)
				break
			}
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

func parseGameserverPorts(gs *models.Gameserver) ([]docker.PortBinding, error) {
	var ports []portMapping
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return nil, fmt.Errorf("parsing gameserver ports: %w", err)
	}

	bindings := make([]docker.PortBinding, len(ports))
	for i, p := range ports {
		bindings[i] = docker.PortBinding{
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
			Protocol:      p.Protocol,
		}
	}
	return bindings, nil
}
