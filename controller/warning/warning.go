package warning

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
)

// WarningSubscriber listens to stats events and emits gameserver.warning events
// when conditions are detected (e.g. storage approaching limits). Deduplicates
// warnings so each condition fires once, and emits a "resolved" when it clears.
type WarningSubscriber struct {
	bus         *controller.EventBus
	settingsSvc *settings.SettingsService
	log         *slog.Logger
	cancel      context.CancelFunc
	wg          sync.WaitGroup

	// active tracks which warnings are currently firing, keyed by "category:gameserverID".
	// Value is the current level ("warning" or "critical").
	active map[string]string
	mu     sync.Mutex
}

func New(bus *controller.EventBus, settingsSvc *settings.SettingsService, log *slog.Logger) *WarningSubscriber {
	return &WarningSubscriber{
		bus:         bus,
		settingsSvc: settingsSvc,
		log:         log,
		active:      make(map[string]string),
	}
}

func (w *WarningSubscriber) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)

	ch, unsub := w.bus.Subscribe()
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok {
					return
				}
				if e, ok := event.(controller.Event); ok && e.Type == controller.EventGameserverStats {
					if stats, ok := e.Data.(*controller.StatsData); ok {
						w.checkStorage(e.GameserverID, stats)
					}
				}
			}
		}
	}()

	w.log.Info("warning subscriber started")
}

func (w *WarningSubscriber) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	w.log.Info("warning subscriber stopped")
}

func (w *WarningSubscriber) checkStorage(gameserverID string, stats *controller.StatsData) {
	if stats.StorageLimitMB == nil || *stats.StorageLimitMB <= 0 {
		return
	}

	limitBytes := int64(*stats.StorageLimitMB) * 1024 * 1024
	pct := int(stats.VolumeSizeBytes * 100 / limitBytes)

	criticalThreshold := w.settingsSvc.GetInt(settings.SettingStorageCriticalThreshold)
	warningThreshold := w.settingsSvc.GetInt(settings.SettingStorageWarningThreshold)

	key := "storage:" + gameserverID

	w.mu.Lock()
	currentLevel := w.active[key]
	w.mu.Unlock()

	var newLevel string
	if pct >= criticalThreshold {
		newLevel = "critical"
	} else if pct >= warningThreshold {
		newLevel = "warning"
	}

	if newLevel == currentLevel {
		return // no change — already fired or already clear
	}

	if newLevel == "" && currentLevel != "" {
		// Condition cleared
		w.mu.Lock()
		delete(w.active, key)
		w.mu.Unlock()

		w.bus.Publish(controller.NewSystemEvent(controller.EventGameserverWarning, gameserverID, &controller.WarningData{
			Category: "storage",
			Level:    "resolved",
			Message:  fmt.Sprintf("Storage usage dropped below %d%%", warningThreshold),
			Extra: map[string]any{
				"used_mb":    stats.VolumeSizeBytes / (1024 * 1024),
				"limit_mb":   *stats.StorageLimitMB,
				"percentage": pct,
			},
		}))
		w.log.Info("storage warning resolved", "gameserver", gameserverID, "percentage", pct)
		return
	}

	if newLevel != "" {
		// New or escalated warning
		w.mu.Lock()
		w.active[key] = newLevel
		w.mu.Unlock()

		msg := fmt.Sprintf("Storage usage at %d%% (%d/%d MB)", pct, stats.VolumeSizeBytes/(1024*1024), *stats.StorageLimitMB)
		w.bus.Publish(controller.NewSystemEvent(controller.EventGameserverWarning, gameserverID, &controller.WarningData{
			Category: "storage",
			Level:    newLevel,
			Message:  msg,
			Extra: map[string]any{
				"used_mb":    stats.VolumeSizeBytes / (1024 * 1024),
				"limit_mb":   *stats.StorageLimitMB,
				"percentage": pct,
			},
		}))
		w.log.Warn("storage warning fired", "gameserver", gameserverID, "level", newLevel, "percentage", pct)
	}
}
