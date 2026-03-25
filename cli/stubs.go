package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// --- Webhooks (TODO: implement when webhook API endpoints are wired) ---

var webhooksCmd = &cobra.Command{
	Use:   "webhooks",
	Short: "Manage webhook endpoints",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("webhooks command not yet implemented")
	},
}

// --- Install (TODO: generate and enable systemd/launchd service) ---

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Install as a system service",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("install command not yet implemented")
	},
}

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
