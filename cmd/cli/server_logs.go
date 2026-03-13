package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var logsCommand = &cobra.Command{
	Use:     "logs",
	GroupID: "server",
	Short:   "Show gamejanitor server logs",
	RunE: func(cmd *cobra.Command, args []string) error {
		tail, _ := cmd.Flags().GetInt("tail")
		path := fmt.Sprintf("/api/logs?tail=%d", tail)

		resp, err := apiGet(path)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var data struct {
			Lines []string `json:"lines"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		for _, line := range data.Lines {
			fmt.Println(line)
		}
		return nil
	},
}

func init() {
	logsCommand.Flags().Int("tail", 100, "Number of lines to show")

	rootCmd.AddCommand(logsCommand)
}
