package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

// --- Start / Stop / Restart ---

var startCmd = &cobra.Command{
	Use:     "start <name-or-id>",
	Short:   "Start a gameserver",
	Example: `  gamejanitor start "My Server"`,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runAction("start", "Starting"),
}

var stopCmd = &cobra.Command{
	Use:     "stop <name-or-id>",
	Short:   "Stop a gameserver",
	Example: `  gamejanitor stop "My Server"`,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runAction("stop", "Stopping"),
}

var restartCmd = &cobra.Command{
	Use:     "restart <name-or-id>",
	Short:   "Restart a gameserver",
	Example: `  gamejanitor restart "My Server"`,
	Args:    cobra.MaximumNArgs(1),
	RunE:    runAction("restart", "Restarting"),
}

var updateGameCmd = &cobra.Command{
	Use:     "update-game <name-or-id>",
	Short:   "Update a gameserver's game to the latest version",
	Example: `  gamejanitor update-game "My Server"`,
	Args:    cobra.ExactArgs(1),
	RunE:    runAction("update-game", "Updating game for"),
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

// sdkAction calls the appropriate SDK method for a single gameserver action.
func sdkAction(action, id string) (*gamejanitor.Gameserver, error) {
	c := getClient()
	switch action {
	case "start":
		return c.Gameservers.Start(ctx(), id)
	case "stop":
		return c.Gameservers.Stop(ctx(), id)
	case "restart":
		return c.Gameservers.Restart(ctx(), id)
	case "update-game":
		return c.Gameservers.UpdateGame(ctx(), id)
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
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

		name := gameserverName(id)

		if !jsonOutput {
			fmt.Printf("%s gameserver %s...\n", verb, name)
		}

		gs, err := sdkAction(action, id)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(gs)
			return nil
		}

		fmt.Printf("Gameserver %s is now %s.\n", name, colorStatus(gs.Status))
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

	req := &gamejanitor.BulkActionRequest{Action: action}
	if all {
		req.All = true
	}
	if nodeID != "" {
		req.NodeID = nodeID
	}

	results, err := getClient().Gameservers.BulkAction(ctx(), req)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(results)
		return nil
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
			fmt.Printf("  + %s (%s): %s\n", r.Name, r.ID[:8], colorStatus(r.Status))
			succeeded++
		}
	}
	fmt.Printf("%s %d/%d gameservers.\n", verb, succeeded, len(results))
	return nil
}

// --- Status ---

var statusCmd = &cobra.Command{
	Use:     "status [name-or-id]",
	Aliases: []string{"ps"},
	Short:   "Show gameserver or cluster status",
	Example: `  gamejanitor status              # all gameservers
  gamejanitor status "My Server"  # single gameserver`,
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

		status, err := getClient().Gameservers.Status(ctx(), gsID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(status)
			return nil
		}

		fmt.Printf("Status:      %s\n", colorStatus(status.Status))
		if status.Container != nil && !status.Container.StartedAt.IsZero() {
			d := time.Since(status.Container.StartedAt)
			fmt.Printf("Uptime:      %s\n", formatDuration(d))
		}

		// Show live query data if the server is running
		query, err := getClient().Gameservers.Query(ctx(), gsID)
		if err == nil && query.PlayersOnline >= 0 {
			fmt.Printf("Players:     %d/%d\n", query.PlayersOnline, query.MaxPlayers)
			if len(query.Players) > 0 {
				fmt.Printf("Online:      %s\n", strings.Join(query.Players, ", "))
			}
			if query.Map != "" {
				fmt.Printf("Map:         %s\n", query.Map)
			}
			if query.Version != "" {
				fmt.Printf("Version:     %s\n", query.Version)
			}
		}
		return nil
	},
}

func runStatusOverview() error {
	resp, err := getClient().Gameservers.List(ctx(), nil)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(resp)
		return nil
	}

	if len(resp.Gameservers) == 0 {
		fmt.Println("No gameservers found.")
		return nil
	}

	w := newTabWriter()
	fmt.Fprintln(w, "NAME\tGAME\tSTATUS\tPLAYERS")
	for _, gs := range resp.Gameservers {
		players := ""
		if gs.Status == "running" || gs.Status == "started" {
			if q, err := getClient().Gameservers.Query(ctx(), gs.ID); err == nil {
				players = fmt.Sprintf("%d/%d", q.PlayersOnline, q.MaxPlayers)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", gs.Name, gs.GameID, colorStatus(gs.Status), players)
	}
	w.Flush()
	return nil
}

// --- Logs ---

var logsCmd = &cobra.Command{
	Use:   "logs <name-or-id>",
	Short: "Show gameserver or service logs",
	Example: `  gamejanitor logs "My Server"
  gamejanitor logs "My Server" -f    # follow live
  gamejanitor logs --service         # gamejanitor service logs`,
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

		logsResp, err := getClient().Gameservers.Logs(ctx(), gsID, tail)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(logsResp)
			return nil
		}

		for _, line := range logsResp.Lines {
			fmt.Println(line)
		}
		return nil
	},
}

func runServiceLogs(cmd *cobra.Command) error {
	tail, _ := cmd.Flags().GetInt("tail")

	lines, err := getClient().Logs.Get(ctx(), tail)
	if err != nil {
		return exitError(err)
	}

	if jsonOutput {
		printJSON(map[string]any{"lines": lines})
		return nil
	}

	for _, line := range lines {
		fmt.Println(line)
	}
	return nil
}

// --- Command ---

var commandCmd = &cobra.Command{
	Use:     "command <name-or-id> <command>",
	Short:   "Send a console command to a gameserver",
	Example: `  gamejanitor command "My Server" "say Hello everyone!"`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		result, err := getClient().Gameservers.SendCommand(ctx(), gsID, args[1])
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(result)
			return nil
		}

		if result.Output != "" {
			fmt.Print(result.Output)
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

		name := gameserverName(id)

		if !confirmAction(fmt.Sprintf("Reinstall gameserver %s?", name)) {
			fmt.Println("Aborted.")
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Reinstalling gameserver %s...\n", name)
		}

		gs, err := getClient().Gameservers.Reinstall(ctx(), id)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(gs)
			return nil
		}

		fmt.Printf("Gameserver %s is now %s.\n", name, colorStatus(gs.Status))
		return nil
	},
}

// --- Migrate ---

var migrateCmd = &cobra.Command{
	Use:     "migrate <name-or-id>",
	Short:   "Migrate a gameserver to another node",
	Example: `  gamejanitor migrate "My Server" --node worker-2`,
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		name := gameserverName(gsID)
		nodeID, _ := cmd.Flags().GetString("node")
		if nodeID == "" {
			return exitError(fmt.Errorf("--node is required"))
		}

		if !confirmAction(fmt.Sprintf("Migrate gameserver %s to node %s?", name, nodeID)) {
			fmt.Println("Aborted.")
			return nil
		}

		if !jsonOutput {
			fmt.Printf("Migrating gameserver %s to node %s...\n", name, nodeID)
		}

		gs, err := getClient().Gameservers.Migrate(ctx(), gsID, nodeID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(gs)
			return nil
		}

		fmt.Printf("Gameserver %s migrated to node %s.\n", name, nodeID)
		return nil
	},
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		hours := int(d.Hours()) % 24
		return fmt.Sprintf("%dd %dh", days, hours)
	}
}
