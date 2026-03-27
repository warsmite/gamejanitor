package warning_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/warning"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func newWarningSubscriber(t *testing.T) (*warning.WarningSubscriber, *controller.EventBus, *settings.SettingsService) {
	t.Helper()
	db := testutil.NewTestDB(t)
	s := store.New(db)
	log := testutil.TestLogger()
	bus := controller.NewEventBus()
	settingsSvc := settings.NewSettingsService(s, log)
	ws := warning.New(bus, settingsSvc, log)
	return ws, bus, settingsSvc
}

func TestWarning_StorageCrossesWarningThreshold(t *testing.T) {
	t.Parallel()
	ws, bus, _ := newWarningSubscriber(t)

	ctx := context.Background()
	ws.Start(ctx)
	defer ws.Stop()

	ch, unsub := bus.Subscribe()
	defer unsub()

	limitMB := 1000
	// Publish stats at 91% — should trigger warning
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-1",
		VolumeSizeBytes: 910 * 1024 * 1024, // 910 MB
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	})

	got := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, got, "should receive a storage warning")
	assert.Equal(t, "storage", got.Category)
	assert.Equal(t, "warning", got.Level)
	assert.Equal(t, "gs-1", got.GameserverID)
}

func TestWarning_StorageDeduplicates(t *testing.T) {
	t.Parallel()
	ws, bus, _ := newWarningSubscriber(t)

	ctx := context.Background()
	ws.Start(ctx)
	defer ws.Stop()

	ch, unsub := bus.Subscribe()
	defer unsub()

	limitMB := 1000
	stats := controller.GameserverStatsEvent{
		GameserverID:    "gs-2",
		VolumeSizeBytes: 950 * 1024 * 1024,
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	}

	// Fire twice
	bus.Publish(stats)
	bus.Publish(stats)

	// Should get exactly one warning
	got := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, got)

	// Second should not fire
	got2 := waitForWarning(t, ch, 500*time.Millisecond)
	assert.Nil(t, got2, "duplicate warning should not fire")
}

func TestWarning_StorageEscalatesToCritical(t *testing.T) {
	t.Parallel()
	ws, bus, _ := newWarningSubscriber(t)

	ctx := context.Background()
	ws.Start(ctx)
	defer ws.Stop()

	ch, unsub := bus.Subscribe()
	defer unsub()

	limitMB := 1000

	// First: warning at 91%
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-3",
		VolumeSizeBytes: 910 * 1024 * 1024,
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	})
	got := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, got)
	assert.Equal(t, "warning", got.Level)

	// Then: critical at 100%
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-3",
		VolumeSizeBytes: 1000 * 1024 * 1024,
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	})
	got2 := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, got2)
	assert.Equal(t, "critical", got2.Level)
}

func TestWarning_StorageResolves(t *testing.T) {
	t.Parallel()
	ws, bus, _ := newWarningSubscriber(t)

	ctx := context.Background()
	ws.Start(ctx)
	defer ws.Stop()

	ch, unsub := bus.Subscribe()
	defer unsub()

	limitMB := 1000

	// Trigger warning
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-4",
		VolumeSizeBytes: 950 * 1024 * 1024,
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	})
	got := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, got)
	assert.Equal(t, "warning", got.Level)

	// Drop below threshold
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-4",
		VolumeSizeBytes: 500 * 1024 * 1024,
		StorageLimitMB:  &limitMB,
		Timestamp:       time.Now(),
	})
	resolved := waitForWarning(t, ch, 2*time.Second)
	require.NotNil(t, resolved)
	assert.Equal(t, "resolved", resolved.Level)
}

func TestWarning_NoLimitNoWarning(t *testing.T) {
	t.Parallel()
	ws, bus, _ := newWarningSubscriber(t)

	ctx := context.Background()
	ws.Start(ctx)
	defer ws.Stop()

	ch, unsub := bus.Subscribe()
	defer unsub()

	// No storage limit — should not warn
	bus.Publish(controller.GameserverStatsEvent{
		GameserverID:    "gs-5",
		VolumeSizeBytes: 999999 * 1024 * 1024,
		StorageLimitMB:  nil,
		Timestamp:       time.Now(),
	})

	got := waitForWarning(t, ch, 500*time.Millisecond)
	assert.Nil(t, got, "should not warn when no storage limit is set")
}

// waitForWarning waits for a GameserverWarningEvent on the channel.
// Returns nil if none received within timeout.
func waitForWarning(t *testing.T, ch <-chan controller.WebhookEvent, timeout time.Duration) *controller.GameserverWarningEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case evt := <-ch:
			if w, ok := evt.(controller.GameserverWarningEvent); ok {
				return &w
			}
			// Skip non-warning events
		case <-deadline:
			return nil
		}
	}
}
