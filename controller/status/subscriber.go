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
	// Status writes are now done synchronously by the lifecycle service and
	// status manager via TransitionStatus (CAS). The subscriber only handles
	// side effects: polling start/stop and operation clearing.
	switch e := event.(type) {
	case controller.InstanceStartedEvent:
		s.startPolling(e.GameserverID)
	case controller.GameserverReadyEvent:
		s.clearOperation(e.GameserverID)
	case controller.InstanceStoppedEvent:
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	case controller.InstanceExitedEvent:
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	case controller.GameserverErrorEvent:
		s.clearOperation(e.GameserverID)
		s.stopPolling(e.GameserverID)
	}
}

func (s *StatusSubscriber) clearOperation(gameserverID string) {
	if s.operations != nil {
		s.operations.ClearOperation(gameserverID)
	}
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

