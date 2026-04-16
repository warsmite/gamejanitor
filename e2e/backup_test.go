//go:build e2e

package e2e

// backup_test.go — backup creation, restore round-trip, and cascade cleanup.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestBackup_RoundTrip writes a marker file, creates a backup, overwrites
// the marker, restores, and verifies the original content returns.
func TestBackup_RoundTrip(t *testing.T) {
	env := NewEnv(t)
	gs := env.NewGameserver()
	gs.Start().MustBeRunning()

	marker := "backup-round-trip-" + gs.ID()
	gs.WriteFile("/data/backup-marker.txt", []byte(marker))

	backup := gs.BackupNow("round-trip")
	backup.MustComplete()

	// Overwrite the marker so we can verify restore brings it back.
	gs.WriteFile("/data/backup-marker.txt", []byte("overwritten"))

	backup.Restore().MustComplete()

	// Restore completed — volume should now have the original marker.
	content := gs.ReadFile("/data/backup-marker.txt")
	assert.True(t, strings.Contains(content, marker),
		"marker file should be restored; got %q", content)
}
