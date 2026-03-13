package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

type ConsoleService struct {
	db     *sql.DB
	docker *docker.Client
	log    *slog.Logger
}

func NewConsoleService(db *sql.DB, dockerClient *docker.Client, log *slog.Logger) *ConsoleService {
	return &ConsoleService{db: db, docker: dockerClient, log: log}
}

// StreamLogs returns a follow-mode log stream for a running gameserver's container.
func (s *ConsoleService) StreamLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, fmt.Errorf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	if !isRunningStatus(gs.Status) {
		return nil, fmt.Errorf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return nil, fmt.Errorf("getting game for gameserver %s: %w", gameserverID, err)
	}
	if game == nil {
		return nil, fmt.Errorf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !HasCapability(game,"console_read") {
		return nil, fmt.Errorf("console_read capability is disabled for game %s", game.Name)
	}

	s.log.Info("streaming logs", "gameserver_id", gameserverID, "container_id", (*gs.ContainerID)[:12])
	return s.docker.ContainerLogs(ctx, *gs.ContainerID, tail, true)
}

// SendCommand executes a command inside a running gameserver's container via /scripts/send-command.
// Returns the command output (stdout), which is relevant for RCON-based games where
// the response comes back on stdout rather than appearing in the container log stream.
func (s *ConsoleService) SendCommand(ctx context.Context, gameserverID string, command string) (string, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return "", fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return "", fmt.Errorf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return "", fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	if !isRunningStatus(gs.Status) {
		return "", fmt.Errorf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return "", fmt.Errorf("getting game for gameserver %s: %w", gameserverID, err)
	}
	if game == nil {
		return "", fmt.Errorf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !HasCapability(game, "console_send") {
		return "", fmt.Errorf("console_send capability is disabled for game %s", game.Name)
	}

	s.log.Info("sending command", "gameserver_id", gameserverID, "command", command)

	exitCode, stdout, stderr, err := s.docker.Exec(ctx, *gs.ContainerID, []string{"/scripts/send-command", command})
	if err != nil {
		return "", fmt.Errorf("executing command in gameserver %s: %w", gameserverID, err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("command failed (exit %d): %s", exitCode, stderr)
	}

	return stdout, nil
}

func isRunningStatus(status string) bool {
	return status == StatusStarted || status == StatusRunning
}

// HasCapability returns true if the capability is NOT in the game's DisabledCapabilities list.
func HasCapability(game *models.Game, capability string) bool {
	if len(game.DisabledCapabilities) == 0 {
		return true
	}

	var disabled []string
	if err := json.Unmarshal(game.DisabledCapabilities, &disabled); err != nil {
		return true
	}

	for _, cap := range disabled {
		if cap == capability {
			return false
		}
	}
	return true
}
