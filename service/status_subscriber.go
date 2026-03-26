package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

// StatusSubscriber listens to lifecycle events on the bus and derives gameserver
// status from them. This centralizes status logic in one place instead of 25+
// scattered setGameserverStatus calls.
type StatusSubscriber struct {
	db     *sql.DB
	log    *slog.Logger
	bus    *EventBus
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Auto-restart crash counter: reset when gameserver reaches "running"
	crashCounts map[string]int
	crashMu     sync.Mutex
}

func NewStatusSubscriber(db *sql.DB, bus *EventBus, log *slog.Logger) *StatusSubscriber {
	return &StatusSubscriber{
		db:          db,
		bus:         bus,
		log:         log,
		crashCounts: make(map[string]int),
	}
}

func (s *StatusSubscriber) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)

	ch, unsub := s.bus.Subscribe()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				s.handleEvent(event)
			}
		}
	}()

	s.log.Info("status subscriber started")
}

func (s *StatusSubscriber) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	s.log.Info("status subscriber stopped")
}

func (s *StatusSubscriber) handleEvent(event WebhookEvent) {
	switch e := event.(type) {
	case ImagePullingEvent:
		s.setStatus(e.GameserverID, StatusInstalling, "")
	case ContainerCreatingEvent:
		s.setStatus(e.GameserverID, StatusStarting, "")
	case ContainerStartedEvent:
		s.setStatus(e.GameserverID, StatusStarted, "")
	case GameserverReadyEvent:
		s.setStatus(e.GameserverID, StatusRunning, "")
		// Reset crash counter on successful run
		s.crashMu.Lock()
		delete(s.crashCounts, e.GameserverID)
		s.crashMu.Unlock()
	case ContainerStoppingEvent:
		s.setStatus(e.GameserverID, StatusStopping, "")
	case ContainerStoppedEvent:
		s.setStatus(e.GameserverID, StatusStopped, "")
	case ContainerExitedEvent:
		s.setStatus(e.GameserverID, StatusError, "Container exited unexpectedly")
	case GameserverErrorEvent:
		s.setStatus(e.GameserverID, StatusError, e.Reason)
	}
}

func (s *StatusSubscriber) setStatus(gameserverID string, newStatus string, errorReason string) {
	gs, err := model.GetGameserver(s.db, gameserverID)
	if err != nil || gs == nil {
		s.log.Error("status subscriber: failed to get gameserver", "id", gameserverID, "error", err)
		return
	}

	oldStatus := gs.Status
	if oldStatus == newStatus {
		return
	}

	gs.Status = newStatus
	if newStatus == StatusError {
		gs.ErrorReason = errorReason
	} else {
		gs.ErrorReason = ""
	}

	if err := model.UpdateGameserver(s.db, gs); err != nil {
		s.log.Error("status subscriber: failed to update gameserver status", "id", gameserverID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}

	s.log.Info("gameserver status changed", "id", gameserverID, "from", oldStatus, "to", newStatus)

	// Publish derived status_changed event for webhook/SSE consumers
	s.bus.Publish(StatusEvent{
		GameserverID: gameserverID,
		OldStatus:    oldStatus,
		NewStatus:    newStatus,
		ErrorReason:  gs.ErrorReason,
		Timestamp:    time.Now(),
	})
}

// IncrementCrashCount increments and returns the crash count for a gameserver.
func (s *StatusSubscriber) IncrementCrashCount(gameserverID string) int {
	s.crashMu.Lock()
	defer s.crashMu.Unlock()
	s.crashCounts[gameserverID]++
	return s.crashCounts[gameserverID]
}
