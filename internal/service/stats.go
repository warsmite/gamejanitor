package service

import (
	"context"
	"database/sql"
	"log/slog"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

type StatsCollector struct {
	db          *sql.DB
	docker      *docker.Client
	broadcaster *EventBroadcaster
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewStatsCollector(db *sql.DB, docker *docker.Client, broadcaster *EventBroadcaster, log *slog.Logger) *StatsCollector {
	return &StatsCollector{
		db:          db,
		docker:      docker,
		broadcaster: broadcaster,
		log:         log,
	}
}

func (c *StatsCollector) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.wg.Add(1)
	go c.loop(ctx)
	c.log.Info("stats collector started")
}

func (c *StatsCollector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.log.Info("stats collector stopped")
}

func (c *StatsCollector) loop(ctx context.Context) {
	defer c.wg.Done()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// Collect once immediately on start
	c.collect(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

func (c *StatsCollector) collect(ctx context.Context) {
	gameservers, err := models.ListGameservers(c.db, models.GameserverFilter{})
	if err != nil {
		c.log.Error("stats collector: listing gameservers", "error", err)
		return
	}

	for _, gs := range gameservers {
		if gs.ContainerID == nil || !isRunningStatus(gs.Status) {
			continue
		}

		stats, err := c.docker.ContainerStats(ctx, *gs.ContainerID)
		if err != nil {
			c.log.Debug("stats collector: failed to get stats", "id", gs.ID, "error", err)
			continue
		}

		c.broadcaster.PublishStats(StatsEvent{
			GameserverID:  gs.ID,
			CPUPercent:    stats.CPUPercent,
			MemoryUsageMB: stats.MemoryUsageMB,
			MemoryLimitMB: stats.MemoryLimitMB,
		})
	}
}
