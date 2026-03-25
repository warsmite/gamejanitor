package testutil

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/warsmite/gamejanitor/db"
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

	if err := db.Migrate(database); err != nil {
		database.Close()
		t.Fatalf("running migrations on test database: %v", err)
	}

	t.Cleanup(func() { database.Close() })

	return database
}
