//go:build e2e

package e2e

// archive_test.go — archive / unarchive workflow and delete-archived.

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestArchive_RoundTrip archives a running gameserver (which auto-stops)
// and then unarchives it back to a node.
func TestArchive_RoundTrip(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()
	gs.Start().MustBeRunning()

	// Write marker to verify data survives the archive round-trip.
	gs.WriteFile("/data/archive-marker.txt", []byte("pre-archive"))

	gs.Archive().MustBeArchived()
	assert.Equal(t, "archived", gs.Snapshot().DesiredState)

	// Unarchive with empty nodeID auto-selects a node.
	gs.Unarchive("").MustBeUnarchived()

	gs.Start().MustBeRunning()
	content := gs.ReadFile("/data/archive-marker.txt")
	assert.Equal(t, "pre-archive", content, "marker should survive archive/unarchive")
}

// TestArchive_DeleteArchived deletes an archived gameserver. The delete
// path should skip worker-side cleanup (no volume/instance to remove) and
// still succeed.
func TestArchive_DeleteArchived(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()
	gs.Start().MustBeRunning()

	gs.Archive().MustBeArchived()
	gs.Delete().MustBeGone()
}
