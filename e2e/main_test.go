//go:build e2e

package e2e

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()

	// Clean up the shared local instance after ALL tests finish.
	// Previously this was in t.Cleanup on the first test, which killed
	// the instance when that one test finished — while others still needed it.
	if localCmd != nil && localCmd.Process != nil {
		localCmd.Process.Signal(os.Interrupt)
		localCmd.Wait()
	}
	cleanupSandboxState()
	if localDir != "" {
		os.RemoveAll(localDir)
	}

	os.Exit(code)
}
