package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start <gameserver>",
	Short: "Start a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE:  runAction("start", "Starting"),
}

var stopCmd = &cobra.Command{
	Use:   "stop <gameserver>",
	Short: "Stop a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE:  runAction("stop", "Stopping"),
}

var restartCmd = &cobra.Command{
	Use:   "restart <gameserver>",
	Short: "Restart a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE:  runAction("restart", "Restarting"),
}

var updateGameCmd = &cobra.Command{
	Use:   "update-game <gameserver>",
	Short: "Update a gameserver's game to the latest version",
	Args:  cobra.ExactArgs(1),
	RunE:  runAction("update-game", "Updating game for"),
}

var reinstallCmd = &cobra.Command{
	Use:   "reinstall <gameserver>",
	Short: "Reinstall a gameserver (preserves data)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		if !confirmAction(fmt.Sprintf("Reinstall gameserver %s?", id[:8])) {
			fmt.Println("Aborted.")
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Reinstalling gameserver %s...\n", id[:8])
		}

		resp, err := apiPost("/api/gameservers/"+id+"/reinstall", nil)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var gs struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &gs); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		fmt.Printf("Gameserver %s is now %s.\n", id[:8], gs.Status)
		return nil
	},
}

func runAction(action, verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		id, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		if !jsonOutput {
			fmt.Printf("%s gameserver %s...\n", verb, id[:8])
		}

		resp, err := apiPost("/api/gameservers/"+id+"/"+action, nil)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var gs struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(resp.Data, &gs); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		fmt.Printf("Gameserver %s is now %s.\n", id[:8], gs.Status)
		return nil
	}
}

var statusCmd = &cobra.Command{
	Use:   "status <gameserver>",
	Short: "Get gameserver status with container info",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		resp, err := apiGet("/api/gameservers/" + gsID + "/status")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var status struct {
			Status    string `json:"status"`
			Container *struct {
				State         string  `json:"state"`
				StartedAt     string  `json:"started_at"`
				MemoryUsageMB int     `json:"memory_usage_mb"`
				MemoryLimitMB int     `json:"memory_limit_mb"`
				CPUPercent    float64 `json:"cpu_percent"`
			} `json:"container"`
		}
		if err := json.Unmarshal(resp.Data, &status); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		fmt.Printf("Status: %s\n", status.Status)
		if status.Container != nil {
			fmt.Printf("Container:\n")
			fmt.Printf("  State:      %s\n", status.Container.State)
			fmt.Printf("  Started:    %s\n", status.Container.StartedAt)
			fmt.Printf("  Memory:     %d / %d MB\n", status.Container.MemoryUsageMB, status.Container.MemoryLimitMB)
			fmt.Printf("  CPU:        %.1f%%\n", status.Container.CPUPercent)
		}
		return nil
	},
}

func init() {
	gameserversCmd.AddCommand(startCmd, stopCmd, restartCmd, updateGameCmd, reinstallCmd, statusCmd)
}
