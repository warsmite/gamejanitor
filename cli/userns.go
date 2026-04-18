package cli

import (
	"fmt"
	"os"
	"syscall"

	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

// MaybeReexecInUserNamespace checks if the current process is not yet inside a
// user namespace. If so, it extracts the userns helper binary and re-execs
// through it to gain CAP_NET_ADMIN for container networking.
//
// This must be called early in the serve path, before any goroutines or
// listeners — syscall.Exec replaces the process. Returns normally if already
// in a user namespace or running as root (no re-exec needed). On successful
// re-exec, this function does not return.
func MaybeReexecInUserNamespace(dataDir string) error {
	if os.Getuid() == 0 || runtime.InUserNamespace() {
		return nil
	}

	usernsBin, err := runtime.ExtractUserns(dataDir)
	if err != nil {
		return fmt.Errorf("extracting userns helper: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving self executable: %w", err)
	}

	argv := append([]string{usernsBin, self}, os.Args[1:]...)
	return syscall.Exec(usernsBin, argv, os.Environ())
}
