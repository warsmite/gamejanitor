package testutil

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
)

// TestGameID is the game ID used by the test game definition in testdata/.
// When creating gameservers manually (not via CreateTestGameserver), remember:
//   - Env must include REQUIRED_VAR: Env: model.Env{"REQUIRED_VAR": "value"}
//   - Set PortMode: "auto" if testing port allocation (Go zero value "" skips allocation — known bug)
const TestGameID = "test-game"

func StrPtr(s string) *string { return &s }

// PastTime returns a time.Time that is `hours` hours in the past.
func PastTime(hours int) time.Time {
	return time.Now().Add(-time.Duration(hours) * time.Hour)
}

// CreateTestGameserver creates a gameserver with sensible defaults for tests that
// need one but aren't testing creation itself. Requires a registered worker.
// The test game definition requires REQUIRED_VAR to be set — this helper provides it.
func CreateTestGameserver(t *testing.T, svc *ServiceBundle) *model.Gameserver {
	t.Helper()
	gs := &model.Gameserver{
		Name:   "Test Gameserver",
		GameID: TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "test-value"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(TestContext(), gs)
	if err != nil {
		t.Fatalf("creating test gameserver: %v", err)
	}
	return gs
}

// SeedVolumeData writes a test file to a gameserver's volume so migration
// verification (VolumeSize > 0) passes. Call after CreateGameserver.
func SeedVolumeData(t *testing.T, worker *FakeWorker, volumeName string) {
	t.Helper()
	if err := worker.WriteFile(context.Background(), volumeName, "server.properties", []byte("test=true\n"), 0644); err != nil {
		t.Fatalf("seeding volume data: %v", err)
	}
}

// SetGameserverDesiredState updates the gameserver's desired_state in the database.
// Use this in tests that need to put a gameserver into a specific desired state
// without going through the full lifecycle.
func SetGameserverDesiredState(t *testing.T, db *store.DB, gameserverID, desiredState string) {
	t.Helper()
	gs, err := db.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		t.Fatalf("getting gameserver for desired state update: %v", err)
	}
	gs.DesiredState = desiredState
	if err := db.UpdateGameserver(gs); err != nil {
		t.Fatalf("setting gameserver desired state: %v", err)
	}
}

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
	return event.SetActorInContext(context.Background(), event.SystemActor)
}

// WaitForEvent subscribes to the event bus and waits for an event of the given type.
// Returns the event or fails the test if the timeout (default 2s) expires.
func WaitForEvent(t *testing.T, bus *event.EventBus, eventType string, timeout ...time.Duration) event.WebhookEvent {
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

// WaitForBackupCompletion polls the backup record until it leaves in_progress state.
// CreateBackup spawns a goroutine; we must wait for it to finish before the test
// returns and t.Cleanup closes the DB, otherwise the goroutine panics.
func WaitForBackupCompletion(t *testing.T, svc *ServiceBundle, backupID string) {
	t.Helper()
	s := store.New(svc.DB)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := s.GetBackup(backupID)
		if err == nil && b != nil && b.Status != "in_progress" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// CollectEvents subscribes to the event bus and collects all events for the given duration.
func CollectEvents(bus *event.EventBus, duration time.Duration) []event.WebhookEvent {
	ch, unsub := bus.Subscribe()
	defer unsub()

	var events []event.WebhookEvent
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
