package main

import (
	"os"

	"github.com/warsmite/gamejanitor/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
