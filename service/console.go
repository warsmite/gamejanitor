package service

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker"
)

type LogSession struct {
	Index   int       `json:"index"`
	ModTime time.Time `json:"mod_time"`
}

type ConsoleService struct {
	db         *sql.DB
	dispatcher *worker.Dispatcher
	gameStore  *games.GameStore
	log        *slog.Logger
}

func NewConsoleService(db *sql.DB, dispatcher *worker.Dispatcher, gameStore *games.GameStore, log *slog.Logger) *ConsoleService {
	return &ConsoleService{db: db, dispatcher: dispatcher, gameStore: gameStore, log: log}
}

// StreamLogs returns a follow-mode log stream for a running gameserver's container.
func (s *ConsoleService) StreamLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, ErrBadRequestf("gameserver %s has no container", gameserverID)
	}
	if !isRunningStatus(gs.Status) {
		return nil, ErrBadRequestf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !HasCapability(game, "console_read") {
		return nil, ErrBadRequestf("console_read capability is disabled for game %s", game.Name)
	}

	s.log.Info("streaming logs", "gameserver_id", gameserverID, "container_id", (*gs.ContainerID)[:12])
	return s.dispatcher.WorkerFor(gameserverID).ContainerLogs(ctx, *gs.ContainerID, tail, true)
}

// SendCommand executes a command inside a running gameserver's container via /scripts/send-command.
// Returns the command output (stdout), which is relevant for RCON-based games where
// the response comes back on stdout rather than appearing in the container log stream.
func (s *ConsoleService) SendCommand(ctx context.Context, gameserverID string, command string) (string, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return "", fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return "", ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return "", ErrBadRequestf("gameserver %s has no container", gameserverID)
	}
	if !isRunningStatus(gs.Status) {
		return "", ErrBadRequestf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return "", ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !HasCapability(game, "command") {
		return "", ErrBadRequestf("command capability is disabled for game %s", game.Name)
	}

	s.log.Info("sending command", "gameserver_id", gameserverID, "command", command)

	exitCode, stdout, stderr, err := s.dispatcher.WorkerFor(gameserverID).Exec(ctx, *gs.ContainerID, []string{"/scripts/send-command", command})
	if err != nil {
		return "", fmt.Errorf("executing command in gameserver %s: %w", gameserverID, err)
	}
	if exitCode != 0 {
		return "", ErrBadRequestf("command failed (exit %d): %s", exitCode, stderr)
	}

	return stdout, nil
}

// ListLogSessions returns available log sessions for a gameserver.
// Index 0 is the most recent session (console.log), 1 is console.log.0, etc.
func (s *ConsoleService) ListLogSessions(ctx context.Context, gameserverID string) ([]LogSession, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	entries, err := w.ListFiles(ctx, gs.VolumeName, ".gamejanitor/logs")
	if err != nil {
		return nil, nil
	}

	var sessions []LogSession
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if e.Name == "console.log" {
			sessions = append(sessions, LogSession{Index: 0, ModTime: e.ModTime})
		} else if strings.HasPrefix(e.Name, "console.log.") {
			suffix := strings.TrimPrefix(e.Name, "console.log.")
			n, err := strconv.Atoi(suffix)
			if err != nil {
				continue
			}
			sessions = append(sessions, LogSession{Index: n + 1, ModTime: e.ModTime})
		}
	}

	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Index < sessions[j].Index })
	return sessions, nil
}

// ReadHistoricalLogs reads persisted logs from the volume for a stopped gameserver.
// session=0 reads console.log (latest), session=1 reads console.log.0, etc.
func (s *ConsoleService) ReadHistoricalLogs(ctx context.Context, gameserverID string, session int, tail int) ([]string, error) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	path := ".gamejanitor/logs/console.log"
	if session > 0 {
		path = fmt.Sprintf(".gamejanitor/logs/console.log.%d", session-1)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	content, err := w.ReadFile(ctx, gs.VolumeName, path)
	if err != nil {
		s.log.Debug("no historical logs found", "gameserver_id", gameserverID, "session", session, "error", err)
		return nil, nil
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return nil, nil
	}
	if tail > 0 && len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return lines, nil
}

// HasCapability returns true if the capability is NOT in the game's DisabledCapabilities list.
func HasCapability(game *games.Game, capability string) bool {
	if len(game.DisabledCapabilities) == 0 {
		return true
	}

	for _, cap := range game.DisabledCapabilities {
		if cap == capability {
			return false
		}
	}
	return true
}
