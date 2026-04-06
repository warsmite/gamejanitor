package proxy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

// GameserverLookup resolves a gameserver by ID with node info populated.
type GameserverLookup interface {
	GetGameserver(id string) (*model.Gameserver, error)
	ListGameservers(ctx context.Context, filter model.GameserverFilter) ([]model.Gameserver, error)
}

// Subscriber listens to the event bus and updates proxy routes when
// gameservers start, stop, or migrate.
type Subscriber struct {
	manager     *Manager
	lookup      GameserverLookup
	localNodeID string // controller's own worker ID — skip proxying for local gameservers
	log         *slog.Logger
	unsub       func()
}

// NewSubscriber creates a proxy subscriber that syncs routes from events.
// localNodeID is the controller's own worker ID (empty if controller-only, no local worker).
// Gameservers on the local node are served directly by the runtime — no proxy needed.
func NewSubscriber(manager *Manager, lookup GameserverLookup, bus *event.EventBus, localNodeID string, log *slog.Logger) *Subscriber {
	s := &Subscriber{
		manager:     manager,
		lookup:      lookup,
		localNodeID: localNodeID,
		log:         log,
	}

	ch, unsub := bus.Subscribe()
	s.unsub = unsub

	go s.listen(ch)
	return s
}

func (s *Subscriber) listen(ch <-chan event.WebhookEvent) {
	for evt := range ch {
		switch evt.EventType() {
		case event.EventInstanceStarted, event.EventGameserverReady:
			s.addRoutes(evt.EventGameserverID())
		case event.EventInstanceStopped, event.EventInstanceExited, event.EventGameserverError:
			s.removeRoutes(evt.EventGameserverID())
		}
	}
}

func (s *Subscriber) addRoutes(gsID string) {
	gs, err := s.lookup.GetGameserver(gsID)
	if err != nil || gs == nil {
		return
	}

	// Skip local gameservers — the runtime binds the ports directly
	if s.localNodeID != "" && gs.NodeID != nil && *gs.NodeID == s.localNodeID {
		return
	}

	if gs.Node == nil || gs.Node.LanIP == "" {
		s.log.Warn("proxy: cannot route, no node LAN IP", "gameserver", gsID)
		return
	}

	for _, p := range gs.Ports {
		backend := fmt.Sprintf("%s:%d", gs.Node.LanIP, int(p.HostPort))
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		if err := s.manager.Set(int(p.HostPort), Route{
			GameserverID: gsID,
			BackendAddr:  backend,
			Protocol:     protocol,
		}); err != nil {
			s.log.Error("proxy: failed to set route", "port", int(p.HostPort), "error", err)
		}
	}
}

func (s *Subscriber) removeRoutes(gsID string) {
	gs, err := s.lookup.GetGameserver(gsID)
	if err != nil || gs == nil {
		return
	}
	for _, p := range gs.Ports {
		s.manager.Remove(int(p.HostPort))
	}
}

// Stop unsubscribes from the event bus.
func (s *Subscriber) Stop() {
	if s.unsub != nil {
		s.unsub()
	}
	s.manager.Stop()
}

// SyncExisting adds proxy routes for all currently running gameservers.
// Call after startup to catch gameservers that were already running.
func (s *Subscriber) SyncExisting(ctx context.Context) {
	gameservers, err := s.lookup.ListGameservers(ctx, model.GameserverFilter{})
	if err != nil {
		s.log.Error("proxy: failed to list gameservers for sync", "error", err)
		return
	}
	for _, gs := range gameservers {
		if gs.Status == controller.StatusRunning || gs.Status == controller.StatusStarting {
			s.addRoutes(gs.ID)
		}
	}
}
