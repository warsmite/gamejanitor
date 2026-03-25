package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// --- Update (TODO: self-update binary from GitHub releases) ---

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Self-update to latest release",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("update command not yet implemented")
	},
}

// --- Init (TODO: generate starter config file with --profile newbie|business) ---

var initConfigCmd = &cobra.Command{
	Use:   "init",
	Short: "Generate a starter config file",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("init command not yet implemented")
	},
}
