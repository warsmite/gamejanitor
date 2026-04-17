package main

import (
	"os"

	"github.com/warsmite/gamejanitor/cli"
	"github.com/warsmite/gamejanitor/worker/local/runtime"
)

func main() {
	if runtime.MaybeHandleNetNSChild() {
		return
	}
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
