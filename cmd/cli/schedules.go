package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var schedulesCmd = &cobra.Command{
	Use:   "schedules",
	Short: "Manage schedules",
}

var schedulesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List schedules for a gameserver",
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, _ := cmd.Flags().GetString("gameserver")
		if gsID == "" {
			return fmt.Errorf("--gameserver flag is required")
		}

		resp, err := apiGet("/api/gameservers/" + gsID + "/schedules")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var schedules []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Type     string `json:"type"`
			CronExpr string `json:"cron_expr"`
			Enabled  bool   `json:"enabled"`
			LastRun  string `json:"last_run"`
			NextRun  string `json:"next_run"`
		}
		if err := json.Unmarshal(resp.Data, &schedules); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if len(schedules) == 0 {
			fmt.Println("No schedules found.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tTYPE\tCRON\tENABLED\tNEXT RUN")
		for _, s := range schedules {
			enabled := "yes"
			if !s.Enabled {
				enabled = "no"
			}
			nextRun := "-"
			if s.NextRun != "" {
				nextRun = s.NextRun
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", s.ID[:8], s.Name, s.Type, s.CronExpr, enabled, nextRun)
		}
		w.Flush()
		return nil
	},
}

var schedulesCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a schedule",
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, _ := cmd.Flags().GetString("gameserver")
		name, _ := cmd.Flags().GetString("name")
		schedType, _ := cmd.Flags().GetString("type")
		cronExpr, _ := cmd.Flags().GetString("cron")
		payload, _ := cmd.Flags().GetString("payload")

		if gsID == "" || name == "" || schedType == "" || cronExpr == "" {
			return fmt.Errorf("--gameserver, --name, --type, and --cron are required")
		}

		body := map[string]any{
			"name":      name,
			"type":      schedType,
			"cron_expr": cronExpr,
		}
		if payload != "" {
			var p json.RawMessage
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				return fmt.Errorf("invalid payload JSON: %w", err)
			}
			body["payload"] = p
		}

		resp, err := apiPost("/api/gameservers/"+gsID+"/schedules", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var schedule struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(resp.Data, &schedule); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		fmt.Printf("Schedule created: %s (%s)\n", schedule.Name, schedule.ID)
		return nil
	},
}

var schedulesUpdateCmd = &cobra.Command{
	Use:   "update <id>",
	Short: "Update a schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		body := map[string]any{}

		if cmd.Flags().Changed("enabled") {
			enabled, _ := cmd.Flags().GetBool("enabled")
			body["enabled"] = enabled
		}
		if cmd.Flags().Changed("cron") {
			cronExpr, _ := cmd.Flags().GetString("cron")
			body["cron_expr"] = cronExpr
		}
		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			body["name"] = name
		}

		if len(body) == 0 {
			return fmt.Errorf("no update flags specified")
		}

		// Need to find the gameserver ID for this schedule — use a workaround via the API
		// For simplicity, require --gameserver
		gsID, _ := cmd.Flags().GetString("gameserver")
		if gsID == "" {
			return fmt.Errorf("--gameserver flag is required")
		}

		resp, err := apiPut("/api/gameservers/"+gsID+"/schedules/"+id, body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Printf("Schedule %s updated.\n", id)
		return nil
	},
}

var schedulesDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		gsID, _ := cmd.Flags().GetString("gameserver")
		if gsID == "" {
			return fmt.Errorf("--gameserver flag is required")
		}

		_, err := apiDelete("/api/gameservers/" + gsID + "/schedules/" + id)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			fmt.Println(`{"status":"ok"}`)
			return nil
		}

		fmt.Printf("Schedule %s deleted.\n", id)
		return nil
	},
}

func init() {
	schedulesListCmd.Flags().String("gameserver", "", "Gameserver ID")
	schedulesCreateCmd.Flags().String("gameserver", "", "Gameserver ID")
	schedulesCreateCmd.Flags().String("name", "", "Schedule name")
	schedulesCreateCmd.Flags().String("type", "", "Schedule type (restart, backup, command, update)")
	schedulesCreateCmd.Flags().String("cron", "", "Cron expression")
	schedulesCreateCmd.Flags().String("payload", "", "JSON payload (for command type)")
	schedulesUpdateCmd.Flags().String("gameserver", "", "Gameserver ID")
	schedulesUpdateCmd.Flags().Bool("enabled", false, "Enable/disable schedule")
	schedulesUpdateCmd.Flags().String("cron", "", "Cron expression")
	schedulesUpdateCmd.Flags().String("name", "", "Schedule name")
	schedulesDeleteCmd.Flags().String("gameserver", "", "Gameserver ID")

	schedulesCmd.AddCommand(schedulesListCmd, schedulesCreateCmd, schedulesUpdateCmd, schedulesDeleteCmd)
}
