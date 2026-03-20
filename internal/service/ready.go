package service

import (
	"context"
	"database/sql"
	"log/slog"
	"regexp"
	"sync"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
)

// ReadyWatcher monitors container logs for a game's ready pattern
// to promote gameservers from Started → Running.
type ReadyWatcher struct {
	db          *sql.DB
	log         *slog.Logger
	broadcaster *EventBroadcaster
	gameStore   *games.GameStore
	querySvc    *QueryService

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
}

func NewReadyWatcher(db *sql.DB, broadcaster *EventBroadcaster, gameStore *games.GameStore, log *slog.Logger) *ReadyWatcher {
	return &ReadyWatcher{
		db:          db,
		log:         log,
		broadcaster: broadcaster,
		gameStore:   gameStore,
		watchers:    make(map[string]context.CancelFunc),
	}
}

func (w *ReadyWatcher) SetQueryService(qs *QueryService) {
	w.querySvc = qs
}

// Watch starts monitoring container logs for the ready pattern.
// If the game has no ready_pattern, promotes immediately.
func (w *ReadyWatcher) Watch(gameserverID string, wkr worker.Worker, containerID string) {
	gs, err := models.GetGameserver(w.db, gameserverID)
	if err != nil || gs == nil {
		w.log.Error("failed to load gameserver for ready watch", "id", gameserverID, "error", err)
		return
	}

	game := w.gameStore.GetGame(gs.GameID)
	if game == nil {
		w.log.Error("game not found for ready watch", "id", gameserverID, "game_id", gs.GameID)
		return
	}

	// Start query polling immediately — the server is running and may
	// already be queryable. The query service handles failures gracefully.
	if w.querySvc != nil {
		w.querySvc.StartPolling(gameserverID)
	}

	var pattern *regexp.Regexp
	if game.ReadyPattern == "" {
		w.log.Info("no ready pattern, promoting immediately", "id", gameserverID)
		w.promote(gameserverID)
	} else {
		var err error
		pattern, err = regexp.Compile(game.ReadyPattern)
		if err != nil {
			w.log.Error("invalid ready pattern, promoting immediately", "id", gameserverID, "pattern", game.ReadyPattern, "error", err)
			w.promote(gameserverID)
			pattern = nil
		}
	}

	// Always watch logs if install hasn't completed yet (to detect install marker)
	if pattern != nil || !gs.Installed {
		ctx, cancel := context.WithCancel(context.Background())

		w.mu.Lock()
		if oldCancel, exists := w.watchers[gameserverID]; exists {
			oldCancel()
		}
		w.watchers[gameserverID] = cancel
		w.mu.Unlock()

		go w.watchLogs(ctx, gameserverID, wkr, containerID, pattern)
	}
}

// Stop cancels any active watcher for a gameserver.
func (w *ReadyWatcher) Stop(gameserverID string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if cancel, exists := w.watchers[gameserverID]; exists {
		cancel()
		delete(w.watchers, gameserverID)
	}
}

// StopAll cancels all active watchers.
func (w *ReadyWatcher) StopAll() {
	w.mu.Lock()
	defer w.mu.Unlock()

	for id, cancel := range w.watchers {
		cancel()
		delete(w.watchers, id)
	}
}

func (w *ReadyWatcher) watchLogs(ctx context.Context, gameserverID string, wkr worker.Worker, containerID string, pattern *regexp.Regexp) {
	defer func() {
		w.mu.Lock()
		delete(w.watchers, gameserverID)
		w.mu.Unlock()
	}()

	if pattern != nil {
		w.log.Info("watching container logs for ready pattern", "id", gameserverID, "pattern", pattern.String())
	} else {
		w.log.Info("watching container logs for install marker", "id", gameserverID)
	}

	reader, err := wkr.ContainerLogs(ctx, containerID, 0, true)
	if err != nil {
		w.log.Error("failed to follow container logs", "id", gameserverID, "error", err)
		if pattern != nil {
			w.promote(gameserverID)
		}
		return
	}
	defer reader.Close()

	lines := make(chan string, 64)
	go worker.ParseLogStream(reader, lines)

	installDetected := false
	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-lines:
			if !ok {
				w.log.Debug("log stream ended", "id", gameserverID)
				return
			}
			if !installDetected && line == "[gamejanitor:installed]" {
				w.markInstalled(gameserverID)
				installDetected = true
				if pattern == nil {
					return
				}
			}
			if pattern != nil && pattern.MatchString(line) {
				w.log.Info("ready pattern matched, promoting to running", "id", gameserverID)
				w.promote(gameserverID)
				return
			}
		}
	}
}

func (w *ReadyWatcher) markInstalled(gameserverID string) {
	gs, err := models.GetGameserver(w.db, gameserverID)
	if err != nil || gs == nil {
		w.log.Error("failed to load gameserver to mark installed", "id", gameserverID, "error", err)
		return
	}
	gs.Installed = true
	if err := models.UpdateGameserver(w.db, gs); err != nil {
		w.log.Error("failed to mark gameserver as installed", "id", gameserverID, "error", err)
		return
	}
	w.log.Info("gameserver marked as installed", "id", gameserverID)
}

func (w *ReadyWatcher) promote(gameserverID string) {
	setGameserverStatus(w.db, w.log, w.broadcaster, gameserverID, StatusRunning, "")
	if w.querySvc != nil {
		w.querySvc.StartPolling(gameserverID)
	}
}
