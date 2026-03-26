package integration_test

import (
	"testing"
	"time"

	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// waitForBackupCompletion polls the backup record until it leaves in_progress state.
// CreateBackup spawns a goroutine; we must wait for it to finish before the test
// returns and t.Cleanup closes the DB, otherwise the goroutine panics.
func waitForBackupCompletion(t *testing.T, svc *testutil.ServiceBundle, backupID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := store.New(svc.DB).GetBackup(backupID)
		if err == nil && b != nil && b.Status != "in_progress" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Goroutine may have finished with error or just be slow — either way, it ran
}
