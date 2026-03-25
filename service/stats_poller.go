package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/worker"
)

const statsPollInterval = 5 * time.Second

// StatsPoller polls container stats for running gameservers and publishes
// GameserverStatsEvent via the EventBus. Also caches the latest stats so
// the GET /stats endpoint can serve them instantly without hitting Docker.
type StatsPoller struct {
	db          *sql.DB
	dispatcher  *worker.Dispatcher
	broadcaster *EventBus
	log         *slog.Logger
	mu          sync.RWMutex
	pollers     map[string]context.CancelFunc
	cache       map[string]*GameserverStatsEvent
}

func NewStatsPoller(db *sql.DB, dispatcher *worker.Dispatcher, broadcaster *EventBus, log *slog.Logger) *StatsPoller {
	return &StatsPoller{
		db:          db,
		dispatcher:  dispatcher,
		broadcaster: broadcaster,
		log:         log,
		pollers:     make(map[string]context.CancelFunc),
		cache:       make(map[string]*GameserverStatsEvent),
	}
}

// GetCachedStats returns the latest polled stats, or nil if not available.
func (s *StatsPoller) GetCachedStats(gameserverID string) *GameserverStatsEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cache[gameserverID]
}

func (s *StatsPoller) StartPolling(gameserverID string) {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	if oldCancel, exists := s.pollers[gameserverID]; exists {
		oldCancel()
	}
	s.pollers[gameserverID] = cancel
	delete(s.cache, gameserverID)
	s.mu.Unlock()

	go s.pollLoop(ctx, gameserverID)
}

func (s *StatsPoller) StopPolling(gameserverID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cancel, exists := s.pollers[gameserverID]; exists {
		cancel()
		delete(s.pollers, gameserverID)
	}
	delete(s.cache, gameserverID)
}

func (s *StatsPoller) StopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, cancel := range s.pollers {
		cancel()
		delete(s.pollers, id)
	}
	s.cache = make(map[string]*GameserverStatsEvent)
	s.log.Info("all stats pollers stopped")
}

func (s *StatsPoller) pollLoop(ctx context.Context, gameserverID string) {
	s.log.Debug("starting stats poll loop", "id", gameserverID)

	// Immediate first poll — no initial delay
	s.pollOnce(ctx, gameserverID)

	ticker := time.NewTicker(statsPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !s.pollOnce(ctx, gameserverID) {
				return
			}
		}
	}
}

// pollOnce fetches stats, caches and publishes. Returns false if polling should stop.
func (s *StatsPoller) pollOnce(ctx context.Context, gameserverID string) bool {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil || gs == nil {
		s.log.Debug("gameserver gone, stopping stats poll", "id", gameserverID)
		return false
	}
	if !isPollableStatus(gs.Status) {
		s.log.Debug("gameserver not in pollable state, stopping stats poll", "id", gameserverID, "status", gs.Status)
		return false
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		s.log.Debug("worker unavailable, stopping stats poll", "id", gameserverID)
		return false
	}
	event := GameserverStatsEvent{
		GameserverID:   gameserverID,
		StorageLimitMB: gs.StorageLimitMB,
		Timestamp:      time.Now(),
	}

	if gs.ContainerID != nil {
		cs, err := w.ContainerStats(ctx, *gs.ContainerID)
		if err == nil {
			event.MemoryUsageMB = cs.MemoryUsageMB
			event.MemoryLimitMB = cs.MemoryLimitMB
			event.CPUPercent = cs.CPUPercent
		}
	}

	volSize, err := w.VolumeSize(ctx, gs.VolumeName)
	if err == nil {
		event.VolumeSizeBytes = volSize
	}

	s.mu.Lock()
	s.cache[gameserverID] = &event
	s.mu.Unlock()

	s.broadcaster.Publish(event)
	return true
}
