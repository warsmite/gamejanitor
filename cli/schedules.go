package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var schedulesCmd = &cobra.Command{
	Use:   "schedules",
	Short: "Manage scheduled tasks",
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

var schedulesListCmd = &cobra.Command{
	Use:   "list <gameserver>",
	Short: "List schedules for a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		schedules, err := getClient().Schedules.List(ctx(), gsID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(schedules)
			return nil
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
			if s.NextRun != nil {
				nextRun = s.NextRun.Format("2006-01-02T15:04:05Z")
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
			return exitError(fmt.Errorf("--name, --type, and --cron are required"))
		}

		req := &gamejanitor.CreateScheduleRequest{
			Name:     name,
			Type:     schedType,
			CronExpr: cronExpr,
		}
		if payload != "" {
			var p json.RawMessage
			if err := json.Unmarshal([]byte(payload), &p); err != nil {
				return exitError(fmt.Errorf("invalid payload JSON: %w", err))
			}
			req.Payload = p
		}

		schedule, err := getClient().Schedules.Create(ctx(), gsID, req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(schedule)
			return nil
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

		req := &gamejanitor.UpdateScheduleRequest{}
		hasUpdate := false
		if cmd.Flags().Changed("enabled") {
			enabled, _ := cmd.Flags().GetBool("enabled")
			req.Enabled = gamejanitor.Ptr(enabled)
			hasUpdate = true
		}
		if cmd.Flags().Changed("cron") {
			cronExpr, _ := cmd.Flags().GetString("cron")
			req.CronExpr = gamejanitor.Ptr(cronExpr)
			hasUpdate = true
		}
		if cmd.Flags().Changed("name") {
			name, _ := cmd.Flags().GetString("name")
			req.Name = gamejanitor.Ptr(name)
			hasUpdate = true
		}

		if !hasUpdate {
			return exitError(fmt.Errorf("no update flags specified"))
		}

		schedule, err := getClient().Schedules.Update(ctx(), gsID, id, req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(schedule)
			return nil
		}

		fmt.Printf("Schedule %s updated.\n", id[:8])
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

		err = getClient().Schedules.Delete(ctx(), gsID, id)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
			return nil
		}

		fmt.Printf("Schedule %s deleted.\n", id[:8])
		return nil
	},
}
