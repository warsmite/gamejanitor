package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Manage backups",
}

var backupsListCmd = &cobra.Command{
	Use:   "list <gameserver>",
	Short: "List backups for a gameserver",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}

		resp, err := apiGet("/api/gameservers/" + gsID + "/backups")
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var backups []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			SizeBytes int64  `json:"size_bytes"`
			CreatedAt string `json:"created_at"`
		}
		if err := json.Unmarshal(resp.Data, &backups); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}

		if len(backups) == 0 {
			fmt.Println("No backups found.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tSIZE\tCREATED")
		for _, b := range backups {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.ID[:8], b.Name, formatBytesStr(b.SizeBytes), b.CreatedAt)
		}
		w.Flush()
		return nil
	},
}

var backupsCreateCmd = &cobra.Command{
	Use:   "create <gameserver>",
	Short: "Create a backup",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		name, _ := cmd.Flags().GetString("name")

		if !jsonOutput {
			fmt.Println("Creating backup...")
		}

		body := map[string]string{}
		if name != "" {
			body["name"] = name
		}

		resp, err := apiPost("/api/gameservers/"+gsID+"/backups", body)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		var backup struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			SizeBytes int64  `json:"size_bytes"`
		}
		if err := json.Unmarshal(resp.Data, &backup); err != nil {
			return fmt.Errorf("parsing response: %w", err)
		}
		fmt.Printf("Backup created: %s (%s, %s)\n", backup.Name, backup.ID, formatBytesStr(backup.SizeBytes))
		return nil
	},
}

var backupsRestoreCmd = &cobra.Command{
	Use:   "restore <gameserver> <backup>",
	Short: "Restore a backup",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		backupID, err := resolveBackupID(gsID, args[1])
		if err != nil {
			return exitError(err)
		}

		if !confirmAction(fmt.Sprintf("Restore backup %s? This will overwrite current gameserver data.", backupID[:8])) {
			fmt.Println("Aborted.")
			return nil
		}

		if !jsonOutput {
			fmt.Println("Restoring backup...")
		}

		resp, err := apiPost("/api/gameservers/"+gsID+"/backups/"+backupID+"/restore", nil)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(resp)
			return nil
		}

		fmt.Println("Backup restored successfully.")
		return nil
	},
}

var backupsDeleteCmd = &cobra.Command{
	Use:   "delete <gameserver> <backup>",
	Short: "Delete a backup",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		backupID, err := resolveBackupID(gsID, args[1])
		if err != nil {
			return exitError(err)
		}

		if !confirmAction(fmt.Sprintf("Delete backup %s?", backupID[:8])) {
			fmt.Println("Aborted.")
			return nil
		}

		_, err = apiDelete("/api/gameservers/" + gsID + "/backups/" + backupID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSONResponse(&apiResponse{Status: "ok"})
			return nil
		}

		fmt.Printf("Backup %s deleted.\n", backupID[:8])
		return nil
	},
}

func formatBytesStr(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func init() {
	backupsCreateCmd.Flags().String("name", "", "Backup name")

	backupsCmd.AddCommand(backupsListCmd, backupsCreateCmd, backupsRestoreCmd, backupsDeleteCmd)
}
