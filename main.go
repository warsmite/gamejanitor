package main

import (
	"os"

	"github.com/warsmite/gamejanitor/cli"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

func main() {
	// Re-exec handlers — must be checked before anything else.
	// The binary re-execs itself as a crun worker (inside pasta's namespace).
	if runtime.MaybeHandleCrunWorker() {
		return
	}
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
