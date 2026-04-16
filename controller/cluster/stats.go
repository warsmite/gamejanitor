package cluster

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

const statsPollInterval = 5 * time.Second
const statsFlushInterval = 30 * time.Second

// StatsHistoryWriter is the interface the poller needs to persist stats samples.
type StatsHistoryWriter interface {
	InsertBatch(samples []model.StatsSample) error
}

// StatsPoller polls instance stats for running gameservers and publishes
// gameserver.stats events via the EventBus. Also caches the latest stats so
// the GET /stats endpoint can serve them instantly without querying the runtime.
type StatsPoller struct {
	store       Store
	dispatcher  *Dispatcher
	broadcaster *event.EventBus
	log         *slog.Logger
	mu          sync.RWMutex
	pollers     map[string]context.CancelFunc
	cache       map[string]*event.StatsData

	// Stats history persistence
	statsWriter    StatsHistoryWriter
	playerCountFn  func(gameserverID string) int
	bufMu       sync.Mutex
	statsBuf    []model.StatsSample
	flusherWg   sync.WaitGroup
	flusherStop context.CancelFunc
}

func NewStatsPoller(store Store, dispatcher *Dispatcher, broadcaster *event.EventBus, statsWriter StatsHistoryWriter, log *slog.Logger) *StatsPoller {
	return &StatsPoller{
		store:       store,
		dispatcher:  dispatcher,
		broadcaster: broadcaster,
		statsWriter: statsWriter,
		log:         log,
		pollers:     make(map[string]context.CancelFunc),
		cache:       make(map[string]*event.StatsData),
	}
}

// SetPlayerCountFn sets a function to look up current player count for a gameserver.
func (s *StatsPoller) SetPlayerCountFn(fn func(string) int) {
	s.playerCountFn = fn
}

// GetCachedStats returns the latest polled stats, or nil if not available.
func (s *StatsPoller) GetCachedStats(gameserverID string) *event.StatsData {
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
	for id, cancel := range s.pollers {
		cancel()
		delete(s.pollers, id)
	}
	s.cache = make(map[string]*event.StatsData)
	s.mu.Unlock()

	// Stop the flusher and wait for final flush
	if s.flusherStop != nil {
		s.flusherStop()
		s.flusherWg.Wait()
	}

	s.log.Info("all stats pollers stopped")
}

// StartFlusher begins the background goroutine that periodically writes buffered
// stats samples to the database. Call after services are started.
func (s *StatsPoller) StartFlusher(ctx context.Context) {
	if s.statsWriter == nil {
		return
	}
	ctx, s.flusherStop = context.WithCancel(ctx)
	s.flusherWg.Add(1)
	go func() {
		defer s.flusherWg.Done()
		ticker := time.NewTicker(statsFlushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				s.flushStats()
				return
			case <-ticker.C:
				s.flushStats()
			}
		}
	}()
}

func (s *StatsPoller) flushStats() {
	s.bufMu.Lock()
	if len(s.statsBuf) == 0 {
		s.bufMu.Unlock()
		return
	}
	batch := s.statsBuf
	s.statsBuf = nil
	s.bufMu.Unlock()

	if err := s.statsWriter.InsertBatch(batch); err != nil {
		s.log.Error("failed to flush stats history", "samples", len(batch), "error", err)
	}
}

func (s *StatsPoller) pollLoop(ctx context.Context, gameserverID string) {
	s.log.Debug("starting stats poll loop", "gameserver", gameserverID)

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
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		s.log.Debug("gameserver gone, stopping stats poll", "gameserver", gameserverID)
		return false
	}
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		s.log.Debug("worker unavailable, stopping stats poll", "gameserver", gameserverID)
		return false
	}
	data := &event.StatsData{
		StorageLimitMB: gs.StorageLimitMB,
	}

	if gs.InstanceID != nil {
		cs, err := w.InstanceStats(ctx, *gs.InstanceID)
		if err == nil {
			data.MemoryUsageMB = cs.MemoryUsageMB
			data.MemoryLimitMB = cs.MemoryLimitMB
			data.CPUPercent = cs.CPUPercent
		}
	}

	volSize, err := w.VolumeSize(ctx, gs.VolumeName)
	if err == nil {
		data.VolumeSizeBytes = volSize
	}

	s.mu.Lock()
	s.cache[gameserverID] = data
	s.mu.Unlock()

	s.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverStats, gameserverID, data))

	// Buffer for history persistence
	if s.statsWriter != nil {
		s.bufMu.Lock()
		var players int
		if s.playerCountFn != nil {
			players = s.playerCountFn(gameserverID)
		}
		now := time.Now()
		s.statsBuf = append(s.statsBuf, model.StatsSample{
			GameserverID:    gameserverID,
			Timestamp:       now,
			CPUPercent:      data.CPUPercent,
			MemoryUsageMB:   data.MemoryUsageMB,
			MemoryLimitMB:   data.MemoryLimitMB,
			VolumeSizeBytes: data.VolumeSizeBytes,
			PlayersOnline:   players,
		})
		s.bufMu.Unlock()
	}

	return true
}
