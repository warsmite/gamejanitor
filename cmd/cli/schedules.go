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
	Use:   "list <gameserver>",
	Short: "List schedules for a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
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
	Use:   "create <gameserver>",
	Short: "Create a schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		name, _ := cmd.Flags().GetString("name")
		schedType, _ := cmd.Flags().GetString("type")
		cronExpr, _ := cmd.Flags().GetString("cron")
		payload, _ := cmd.Flags().GetString("payload")

		if name == "" || schedType == "" || cronExpr == "" {
			return fmt.Errorf("--name, --type, and --cron are required")
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
	Use:   "update <gameserver> <schedule>",
	Short: "Update a schedule",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		id, err := resolveScheduleID(gsID, args[1])
		if err != nil {
			return exitError(err)
		}
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
	Use:   "delete <gameserver> <schedule>",
	Short: "Delete a schedule",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		id, err := resolveScheduleID(gsID, args[1])
		if err != nil {
			return exitError(err)
		}

		if !confirmAction(fmt.Sprintf("Delete schedule %s?", id[:8])) {
			fmt.Println("Aborted.")
			return nil
		}

		_, err = apiDelete("/api/gameservers/" + gsID + "/schedules/" + id)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(&apiResponse{Status: "ok"})
			return nil
		}

		fmt.Printf("Schedule %s deleted.\n", id[:8])
		return nil
	},
}

func init() {
	schedulesCreateCmd.Flags().String("name", "", "Schedule name")
	schedulesCreateCmd.Flags().String("type", "", "Schedule type (restart, backup, command, update)")
	schedulesCreateCmd.Flags().String("cron", "", "Cron expression")
	schedulesCreateCmd.Flags().String("payload", "", "JSON payload (for command type)")
	schedulesUpdateCmd.Flags().Bool("enabled", false, "Enable/disable schedule")
	schedulesUpdateCmd.Flags().String("cron", "", "Cron expression")
	schedulesUpdateCmd.Flags().String("name", "", "Schedule name")

	schedulesCmd.AddCommand(schedulesListCmd, schedulesCreateCmd, schedulesUpdateCmd, schedulesDeleteCmd)
}
