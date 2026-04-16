package testutil

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"testing"

	"github.com/warsmite/gamejanitor/store"
)

// NewTestDB returns an in-memory SQLite database with all migrations applied.
// Each call creates a fresh, isolated database. The DB is closed when the test finishes.
func NewTestDB(t *testing.T) *sql.DB {
	t.Helper()

	// Unique name per test so parallel tests don't share state.
	// file::memory: is shared across connections; named memory DBs are per-name.
	name := fmt.Sprintf("file:%s?mode=memory&cache=shared&_pragma=foreign_keys(ON)", t.Name())
	database, err := sql.Open("sqlite", name)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}

	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	if err := store.Migrate(database); err != nil {
		database.Close()
		t.Fatalf("running migrations on test database: %v", err)
	}

	t.Cleanup(func() { database.Close() })

	return database
}

// StrPtr returns a pointer to s. Helper for building model values in tests
// where optional string fields are *string.
func StrPtr(s string) *string { return &s }

// TestLogger returns a quiet logger for tests. Set DEBUG_TESTS=1 to see output.
func TestLogger() *slog.Logger {
	level := slog.LevelError
	if os.Getenv("DEBUG_TESTS") != "" {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}
