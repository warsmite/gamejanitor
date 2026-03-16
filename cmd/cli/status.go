package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var overviewStatusCmd = &cobra.Command{
	Use:     "status",
	GroupID: "server",
	Short:   "Show system status overview",
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := apiGet("/api/status")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var data struct {
			Gameservers []struct {
				ID            string  `json:"id"`
				Name          string  `json:"name"`
				GameID        string  `json:"game_id"`
				Status        string  `json:"status"`
				MemoryUsageMB int     `json:"memory_usage_mb"`
				MemoryLimitMB int     `json:"memory_limit_mb"`
				CPUPercent    float64 `json:"cpu_percent"`
				PlayersOnline *int    `json:"players_online"`
				MaxPlayers    *int    `json:"max_players"`
			} `json:"gameservers"`
			Summary struct {
				Total   int `json:"total"`
				Running int `json:"running"`
				Stopped int `json:"stopped"`
			} `json:"summary"`
		}
		if err := json.Unmarshal(resp.Data, &data); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("Gameservers: %d total (%d running, %d stopped)\n\n",
			data.Summary.Total, data.Summary.Running, data.Summary.Stopped)

		if len(data.Gameservers) == 0 {
			fmt.Println("No gameservers configured.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "NAME\tGAME\tSTATUS\tCPU\tMEMORY\tPLAYERS")
		for _, gs := range data.Gameservers {
			cpu := "-"
			memory := "-"
			players := "-"

			isRunning := gs.Status == "started" || gs.Status == "running"
			if isRunning {
				cpu = fmt.Sprintf("%.1f%%", gs.CPUPercent)
				if gs.MemoryLimitMB > 0 {
					memory = fmt.Sprintf("%s / %s", formatMemory(gs.MemoryUsageMB), formatMemory(gs.MemoryLimitMB))
				} else {
					memory = formatMemory(gs.MemoryUsageMB)
				}
				if gs.PlayersOnline != nil {
					players = fmt.Sprintf("%d/%d", *gs.PlayersOnline, *gs.MaxPlayers)
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				gs.Name, gs.GameID, gs.Status, cpu, memory, players)
		}
		w.Flush()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(overviewStatusCmd)
}
