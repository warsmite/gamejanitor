package status

import (
	"context"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller"
)

// OperationClearer clears the active operation for a gameserver.
// Implemented by gameserver.OperationTracker.
type OperationClearer interface {
	ClearOperation(gameserverID string)
}

// StatusSubscriber listens to lifecycle events on the bus and derives gameserver
// status from them. This centralizes status logic in one place instead of 25+
// scattered setGameserverStatus calls.
type StatusSubscriber struct {
	store       Store
	log         *slog.Logger
	bus         *controller.EventBus
	querySvc    *QueryService
	statsPoller *StatsPoller
	operations  OperationClearer
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func (s *StatusSubscriber) SetOperationClearer(oc OperationClearer) {
	s.operations = oc
}

func NewStatusSubscriber(store Store, bus *controller.EventBus, querySvc *QueryService, statsPoller *StatsPoller, log *slog.Logger) *StatusSubscriber {
	return &StatusSubscriber{
		store:       store,
		bus:         bus,
		querySvc:    querySvc,
		statsPoller: statsPoller,
		log:         log,
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

func (s *StatusSubscriber) handleEvent(event controller.WebhookEvent) {
	switch e := event.(type) {
	case controller.ImagePullingEvent:
		s.setStatus(e.GameserverID, controller.StatusInstalling, "")
	case controller.InstanceCreatingEvent:
		s.setStatus(e.GameserverID, controller.StatusStarting, "")
	case controller.InstanceStartedEvent:
		s.setStatus(e.GameserverID, controller.StatusStarted, "")
	case controller.GameserverReadyEvent:
		s.setStatus(e.GameserverID, controller.StatusRunning, "")
		s.clearOperation(e.GameserverID)
		s.startPolling(e.GameserverID)
	case controller.InstanceStoppingEvent:
		s.setStatus(e.GameserverID, controller.StatusStopping, "")
	case controller.InstanceStoppedEvent:
		s.setStatus(e.GameserverID, controller.StatusStopped, "")
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	case controller.InstanceExitedEvent:
		s.setStatus(e.GameserverID, controller.StatusError, "Instance exited unexpectedly")
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	case controller.GameserverErrorEvent:
		s.setStatus(e.GameserverID, controller.StatusError, e.Reason)
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	}
}

func (s *StatusSubscriber) clearOperation(gameserverID string) {
	if s.operations != nil {
		s.operations.ClearOperation(gameserverID)
	}
}

func (s *StatusSubscriber) setStatus(gameserverID string, newStatus string, errorReason string) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		s.log.Error("status subscriber: failed to get gameserver", "gameserver", gameserverID, "error", err)
		return
	}

	oldStatus := gs.Status
	if oldStatus == newStatus {
		return
	}

	if newStatus != controller.StatusError {
		errorReason = ""
	}

	// Record status as a status_changed activity instead of writing to the gameserver table
	if err := setGameserverStatus(s.store, gameserverID, newStatus, errorReason); err != nil {
		s.log.Error("status subscriber: failed to record status_changed activity", "gameserver", gameserverID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}

	s.log.Info("gameserver status changed", "gameserver", gameserverID, "from", oldStatus, "to", newStatus)
}

func (s *StatusSubscriber) startPolling(gameserverID string) {
	if s.querySvc != nil {
		s.querySvc.StartPolling(gameserverID)
	}
	if s.statsPoller != nil {
		s.statsPoller.StartPolling(gameserverID)
	}
}

func (s *StatusSubscriber) stopPolling(gameserverID string) {
	if s.querySvc != nil {
		s.querySvc.StopPolling(gameserverID)
	}
	if s.statsPoller != nil {
		s.statsPoller.StopPolling(gameserverID)
	}
}

// setGameserverStatus updates the status and error_reason columns directly on the gameserver row.
func setGameserverStatus(store Store, gameserverID, newStatus, errorReason string) error {
	gs, err := store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		return err
	}
	gs.Status = newStatus
	gs.ErrorReason = errorReason
	return store.UpdateGameserver(gs)
}

