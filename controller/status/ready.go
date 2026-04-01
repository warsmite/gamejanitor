package status

import (
	"context"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/logparse"
)

// ReadyWatcher monitors instance logs for a game's ready pattern
// to promote gameservers from Started → Running.
type ReadyWatcher struct {
	store       Store
	log         *slog.Logger
	broadcaster *controller.EventBus
	gameStore   *games.GameStore

	mu       sync.Mutex
	watchers map[string]context.CancelFunc
}

func NewReadyWatcher(store Store, broadcaster *controller.EventBus, gameStore *games.GameStore, log *slog.Logger) *ReadyWatcher {
	return &ReadyWatcher{
		store:       store,
		log:         log,
		broadcaster: broadcaster,
		gameStore:   gameStore,
		watchers:    make(map[string]context.CancelFunc),
	}
}

// Watch starts monitoring instance logs for the ready pattern.
// If the game has no ready_pattern, promotes immediately.
func (w *ReadyWatcher) Watch(gameserverID string, wkr worker.Worker, instanceID string) {
	gs, err := w.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		w.log.Error("failed to load gameserver for ready watch", "gameserver", gameserverID, "error", err)
		return
	}

	game := w.gameStore.GetGame(gs.GameID)
	if game == nil {
		w.log.Error("game not found for ready watch", "gameserver", gameserverID, "game_id", gs.GameID)
		return
	}

	var pattern *regexp.Regexp
	if game.ReadyPattern == "" {
		w.log.Info("no ready pattern, promoting immediately", "gameserver", gameserverID)
		w.promote(gameserverID)
	} else {
		var err error
		pattern, err = regexp.Compile(game.ReadyPattern)
		if err != nil {
			w.log.Error("invalid ready pattern, promoting immediately", "gameserver", gameserverID, "pattern", game.ReadyPattern, "error", err)
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

		go w.watchLogs(ctx, gameserverID, wkr, instanceID, pattern)
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

func (w *ReadyWatcher) watchLogs(ctx context.Context, gameserverID string, wkr worker.Worker, instanceID string, pattern *regexp.Regexp) {
	defer func() {
		w.mu.Lock()
		delete(w.watchers, gameserverID)
		w.mu.Unlock()
	}()

	if pattern != nil {
		w.log.Info("watching instance logs for ready pattern", "gameserver", gameserverID, "pattern", pattern.String())
	} else {
		w.log.Info("watching instance logs for install marker", "gameserver", gameserverID)
	}

	reader, err := wkr.InstanceLogs(ctx, instanceID, 0, true)
	if err != nil {
		w.log.Error("failed to follow instance logs", "gameserver", gameserverID, "error", err)
		if pattern != nil {
			w.promote(gameserverID)
		}
		return
	}
	defer reader.Close()

	lines := make(chan string, 64)
	go logparse.ParseLogStream(reader, lines)

	installDetected := false
	for {
		select {
		case <-ctx.Done():
			return

		case line, ok := <-lines:
			if !ok {
				w.log.Debug("log stream ended", "gameserver", gameserverID)
				return
			}
			if !installDetected && line == controller.InstallMarker {
				w.markInstalled(gameserverID)
				installDetected = true
				if pattern == nil {
					return
				}
			}
			if pattern != nil && pattern.MatchString(line) {
				w.log.Info("ready pattern matched, promoting to running", "gameserver", gameserverID)
				w.promote(gameserverID)
				return
			}
		}
	}
}

func (w *ReadyWatcher) markInstalled(gameserverID string) {
	gs, err := w.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		w.log.Error("failed to load gameserver to mark installed", "gameserver", gameserverID, "error", err)
		return
	}
	gs.Installed = true
	if err := w.store.UpdateGameserver(gs); err != nil {
		w.log.Error("failed to mark gameserver as installed", "gameserver", gameserverID, "error", err)
		return
	}
	w.log.Info("gameserver marked as installed", "gameserver", gameserverID)
}

func (w *ReadyWatcher) promote(gameserverID string) {
	// Publish the ready event — StatusManager picks it up and sets ready=true
	// in the runtime state map. No DB write needed.
	w.broadcaster.Publish(controller.GameserverReadyEvent{GameserverID: gameserverID, Timestamp: time.Now()})
}
