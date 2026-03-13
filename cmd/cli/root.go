package cli

import (
	"github.com/spf13/cobra"
)

var (
	jsonOutput       bool
	apiURL           string
	skipConfirmation bool
)

var rootCmd = &cobra.Command{
	Use:   "gamejanitor",
	Short: "Local game server hosting tool",
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	rootCmd.PersistentFlags().StringVar(&apiURL, "api-url", "http://localhost:8080", "API base URL")
	rootCmd.PersistentFlags().BoolVarP(&skipConfirmation, "yes", "y", false, "Skip confirmation prompts")

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(gamesCmd)
	rootCmd.AddCommand(gameserversCmd)
	rootCmd.AddCommand(schedulesCmd)
	rootCmd.AddCommand(backupsCmd)
}

func Execute() error {
	return rootCmd.Execute()
}
