//go:build e2e

package e2e

// migration_test.go — moving gameservers between nodes. Requires 2+ workers.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigration_RunningServer(t *testing.T) {
	env := NewEnv(t)
	workers := env.RequireMultiNode()

	gs := env.NewGameserver(map[string]any{"memory_limit_mb": 2048})
	gs.Start().MustBeRunning()

	source := *gs.Snapshot().NodeID
	target := otherNode(workers, source)
	require.NotEmpty(t, target, "should have a different worker to migrate to")

	// Files must survive the migration — write a marker to verify.
	gs.WriteFile("/data/migration-marker.txt", []byte("before-migration"))

	gs.Migrate(target).MustMigrateTo(target)

	// Running servers auto-start on the target after migration.
	gs.Start().MustBeRunning() // no-op if already running, but ensures state

	// Verify the marker survived.
	content := gs.ReadFile("/data/migration-marker.txt")
	assert.Equal(t, "before-migration", content, "marker file should survive migration")
}

func TestMigration_StoppedServer(t *testing.T) {
	env := NewEnv(t)
	workers := env.RequireMultiNode()

	gs := env.NewGameserver(map[string]any{"memory_limit_mb": 2048})

	// Start and stop so the volume is populated, then migrate stopped.
	gs.Start().MustBeRunning()
	gs.Stop().MustBeStopped()

	source := *gs.Snapshot().NodeID
	target := otherNode(workers, source)

	gs.Migrate(target).MustMigrateTo(target)

	snap := gs.Snapshot()
	assert.Equal(t, "stopped", snap.Status, "stopped server should stay stopped after migration")
	assert.Equal(t, target, *snap.NodeID, "should be on target node")
}

// otherNode returns any worker ID from the set that isn't `not`.
func otherNode(workers []string, not string) string {
	for _, w := range workers {
		if w != not {
			return w
		}
	}
	return ""
}
