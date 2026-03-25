package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

// --- Start / Stop / Restart ---

var startCmd = &cobra.Command{
	Use:   "start <name-or-id>",
	Short: "Start a gameserver",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAction("start", "Starting"),
}

var stopCmd = &cobra.Command{
	Use:   "stop <name-or-id>",
	Short: "Stop a gameserver",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAction("stop", "Stopping"),
}

var restartCmd = &cobra.Command{
	Use:   "restart <name-or-id>",
	Short: "Restart a gameserver",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runAction("restart", "Restarting"),
}

var updateGameCmd = &cobra.Command{
	Use:   "update-game <name-or-id>",
	Short: "Update a gameserver's game to the latest version",
	Args:  cobra.ExactArgs(1),
	RunE:  runAction("update-game", "Updating game for"),
}

func init() {
	for _, cmd := range []*cobra.Command{startCmd, stopCmd, restartCmd} {
		cmd.Flags().Bool("all", false, "Apply to all gameservers")
		cmd.Flags().String("node", "", "Apply to all gameservers on a specific node")
	}
	logsCmd.Flags().Int("tail", 100, "Number of lines to show")
	logsCmd.Flags().BoolP("follow", "f", false, "Stream live logs")
	logsCmd.Flags().Bool("service", false, "Show gamejanitor service logs instead of gameserver logs")
	migrateCmd.Flags().String("node", "", "Target worker node ID")
}

func runAction(action, verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		all, _ := cmd.Flags().GetBool("all")
		nodeID, _ := cmd.Flags().GetString("node")

		if all || nodeID != "" {
			return runBulkAction(action, verb, all, nodeID)
		}

		if len(args) == 0 {
			return exitError(fmt.Errorf("requires a gameserver argument, or --all / --node for bulk"))
		}

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

func runBulkAction(action, verb string, all bool, nodeID string) error {
	scope := "all gameservers"
	if nodeID != "" {
		scope = fmt.Sprintf("all gameservers on node %s", nodeID)
	}

	if !confirmAction(fmt.Sprintf("%s %s?", verb, scope)) {
		fmt.Println("Aborted.")
		return nil
	}

	body := map[string]any{"action": action}
	if all {
		body["all"] = true
	}
	if nodeID != "" {
		body["node_id"] = nodeID
	}

	resp, err := apiPost("/api/gameservers/bulk", body)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSONResponse(resp)
		return nil
	}

	var results []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(resp.Data, &results); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if len(results) == 0 {
		fmt.Println("No gameservers matched.")
		return nil
	}

	succeeded := 0
	for _, r := range results {
		if r.Error != "" {
			fmt.Printf("  x %s (%s): %s\n", r.Name, r.ID[:8], r.Error)
		} else {
			fmt.Printf("  + %s (%s): %s\n", r.Name, r.ID[:8], r.Status)
			succeeded++
		}
	}
	fmt.Printf("%s %d/%d gameservers.\n", verb, succeeded, len(results))
	return nil
}

// --- Status ---

var statusCmd = &cobra.Command{
	Use:   "status [name-or-id]",
	Short: "Show gameserver or cluster status",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// No argument: cluster overview (list all gameservers with status)
		if len(args) == 0 {
			return runStatusOverview()
		}

		// With argument: detailed status for one gameserver
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
			fmt.Printf("  Memory:     %s / %s\n", formatMemory(status.Container.MemoryUsageMB), formatMemory(status.Container.MemoryLimitMB))
			fmt.Printf("  CPU:        %.1f%%\n", status.Container.CPUPercent)
		}
		return nil
	},
}

func runStatusOverview() error {
	resp, err := apiGet("/api/gameservers")
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSONResponse(resp)
		return nil
	}

	var gameservers []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		GameID string `json:"game_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(resp.Data, &gameservers); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}

	if len(gameservers) == 0 {
		fmt.Println("No gameservers found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "NAME\tGAME\tSTATUS")
	for _, gs := range gameservers {
		fmt.Fprintf(w, "%s\t%s\t%s\n", gs.Name, gs.GameID, gs.Status)
	}
	w.Flush()
	return nil
}

// --- Logs ---

var logsCmd = &cobra.Command{
	Use:   "logs <name-or-id>",
	Short: "Show gameserver or service logs",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		isService, _ := cmd.Flags().GetBool("service")
		if isService {
			return runServiceLogs(cmd)
		}

		if len(args) == 0 {
			return exitError(fmt.Errorf("requires a gameserver argument, or use --service for gamejanitor logs"))
		}

		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		tail, _ := cmd.Flags().GetInt("tail")
		follow, _ := cmd.Flags().GetBool("follow")

		if follow {
			// TODO: --follow needs a streaming API endpoint (not yet implemented server-side)
			return exitError(fmt.Errorf("--follow is not yet supported (requires streaming API endpoint)"))
		}

		path := fmt.Sprintf("/api/gameservers/%s/logs?tail=%d", gsID, tail)

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

func runServiceLogs(cmd *cobra.Command) error {
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
}

// --- Command ---

var commandCmd = &cobra.Command{
	Use:   "command <name-or-id> <command>",
	Short: "Send a console command to a gameserver",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		body := map[string]string{"command": args[1]}
		resp, err := apiPost("/api/gameservers/"+gsID+"/command", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var output struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal(resp.Data, &output); err == nil && output.Output != "" {
			fmt.Print(output.Output)
		} else {
			fmt.Println("Command sent.")
		}
		return nil
	},
}

// --- Reinstall ---

var reinstallCmd = &cobra.Command{
	Use:   "reinstall <name-or-id>",
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

// --- Migrate ---

var migrateCmd = &cobra.Command{
	Use:   "migrate <name-or-id>",
	Short: "Migrate a gameserver to another node",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		nodeID, _ := cmd.Flags().GetString("node")
		if nodeID == "" {
			return exitError(fmt.Errorf("--node is required"))
		}

		if !confirmAction(fmt.Sprintf("Migrate gameserver %s to node %s?", gsID[:8], nodeID)) {
			fmt.Println("Aborted.")
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Migrating gameserver %s to node %s...\n", gsID[:8], nodeID)
		}

		body := map[string]string{"node_id": nodeID}
		resp, err := apiPost("/api/gameservers/"+gsID+"/migrate", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Gameserver %s migrated to node %s.\n", gsID[:8], nodeID)
		return nil
	},
}
