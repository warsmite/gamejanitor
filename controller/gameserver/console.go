package gameserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

type LogSession struct {
	Index   int       `json:"index"`
	ModTime time.Time `json:"mod_time"`
}

// ConsoleStore abstracts the database operations the console service needs.
type ConsoleStore interface {
	GetGameserver(id string) (*model.Gameserver, error)
}

type ConsoleService struct {
	store      ConsoleStore
	dispatcher *orchestrator.Dispatcher
	gameStore  *games.GameStore
	log        *slog.Logger
}

func NewConsoleService(store ConsoleStore, dispatcher *orchestrator.Dispatcher, gameStore *games.GameStore, log *slog.Logger) *ConsoleService {
	return &ConsoleService{store: store, dispatcher: dispatcher, gameStore: gameStore, log: log}
}

// StreamLogs returns a follow-mode log stream for a running gameserver's container.
func (s *ConsoleService) StreamLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.InstanceID == nil {
		return nil, controller.ErrBadRequestf("gameserver %s has no container", gameserverID)
	}
	if !controller.IsRunningStatus(gs.Status) {
		return nil, controller.ErrBadRequestf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !game.HasCapability("console_read") {
		return nil, controller.ErrBadRequestf("console_read capability is disabled for game %s", game.Name)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	s.log.Info("streaming logs", "gameserver", gameserverID, "instance_id", (*gs.InstanceID)[:12])
	return w.InstanceLogs(ctx, *gs.InstanceID, tail, true)
}

// SendCommand executes a command inside a running gameserver's container via /scripts/send-command.
// Returns the command output (stdout), which is relevant for RCON-based games where
// the response comes back on stdout rather than appearing in the container log stream.
func (s *ConsoleService) SendCommand(ctx context.Context, gameserverID string, command string) (string, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return "", fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return "", controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.InstanceID == nil {
		return "", controller.ErrBadRequestf("gameserver %s has no container", gameserverID)
	}
	if !controller.IsRunningStatus(gs.Status) {
		return "", controller.ErrBadRequestf("gameserver %s is not running (status: %s)", gameserverID, gs.Status)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return "", controller.ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, gameserverID)
	}
	if !game.HasCapability("command") {
		return "", controller.ErrBadRequestf("command capability is disabled for game %s", game.Name)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return "", controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}

	s.log.Info("sending command", "gameserver", gameserverID, "command", command)

	exitCode, stdout, stderr, err := w.Exec(ctx, *gs.InstanceID, []string{"/scripts/send-command", command})
	if err != nil {
		return "", fmt.Errorf("executing command in gameserver %s: %w", gameserverID, err)
	}
	if exitCode != 0 {
		return "", controller.ErrBadRequestf("command failed (exit %d): %s", exitCode, stderr)
	}

	return stdout, nil
}

// ListLogSessions returns available log sessions for a gameserver.
// Index 0 is the most recent session (console.log), 1 is console.log.0, etc.
func (s *ConsoleService) ListLogSessions(ctx context.Context, gameserverID string) ([]LogSession, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
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
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	path := ".gamejanitor/logs/console.log"
	if session > 0 {
		path = fmt.Sprintf(".gamejanitor/logs/console.log.%d", session-1)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	content, err := w.ReadFile(ctx, gs.VolumeName, path)
	if err != nil {
		s.log.Debug("no historical logs found", "gameserver", gameserverID, "session", session, "error", err)
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
