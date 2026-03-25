package testutil

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/service"
)

// TestGameID is the game ID used by the test game definition in testdata/.
const TestGameID = "test-game"

// testdataDir returns the absolute path to the testdata/ directory.
func testdataDir() string {
	_, filename, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(filename), "..", "testdata")
}

// NewTestGameStore loads a GameStore that includes the test-game definition from testdata/.
// Real embedded games are also loaded (via NewGameStore) but tests should use TestGameID.
func NewTestGameStore(t *testing.T) *games.GameStore {
	t.Helper()

	gamesDir := filepath.Join(testdataDir(), "games")
	store, err := games.NewGameStore(gamesDir, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatalf("creating test game store: %v", err)
	}

	if store.GetGame(TestGameID) == nil {
		t.Fatalf("test game %q not found in game store", TestGameID)
	}

	return store
}

// TestLogger returns a quiet logger for tests. Set DEBUG_TESTS=1 to see output.
func TestLogger() *slog.Logger {
	level := slog.LevelError
	if os.Getenv("DEBUG_TESTS") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

// TestContext returns a context with a system actor set (for event publishing).
func TestContext() context.Context {
	return service.SetActorInContext(context.Background(), service.SystemActor)
}

// WaitForEvent subscribes to the event bus and waits for an event of the given type.
// Returns the event or fails the test if the timeout (default 2s) expires.
func WaitForEvent(t *testing.T, bus *service.EventBus, eventType string, timeout ...time.Duration) service.WebhookEvent {
	t.Helper()

	d := 2 * time.Second
	if len(timeout) > 0 {
		d = timeout[0]
	}

	ch, unsub := bus.Subscribe()
	defer unsub()

	timer := time.NewTimer(d)
	defer timer.Stop()

	for {
		select {
		case evt := <-ch:
			if evt.EventType() == eventType {
				return evt
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for event %q after %s", eventType, d)
			return nil
		}
	}
}

// CollectEvents subscribes to the event bus and collects all events for the given duration.
func CollectEvents(bus *service.EventBus, duration time.Duration) []service.WebhookEvent {
	ch, unsub := bus.Subscribe()
	defer unsub()

	var events []service.WebhookEvent
	timer := time.NewTimer(duration)
	defer timer.Stop()

	for {
		select {
		case evt := <-ch:
			events = append(events, evt)
		case <-timer.C:
			return events
		}
	}
}
