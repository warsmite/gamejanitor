package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	gamejanitor "github.com/warsmite/gamejanitor/sdk"
)

var backupsCmd = &cobra.Command{
	Use:   "backups",
	Short: "Manage backups",
}

func init() {
	backupsCreateCmd.Flags().String("name", "", "Backup name")
	backupsCmd.AddCommand(backupsListCmd, backupsCreateCmd, backupsRestoreCmd, backupsDeleteCmd, backupsDownloadCmd)
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

		backups, err := getClient().Backups.List(ctx(), gsID, nil)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(backups)
			return nil
		}

		if len(backups) == 0 {
			fmt.Println("No backups found.")
			return nil
		}

		w := newTabWriter()
		fmt.Fprintln(w, "ID\tNAME\tSIZE\tCREATED")
		for _, b := range backups {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", b.ID[:8], b.Name, formatBytes(b.SizeBytes), b.CreatedAt.Format("2006-01-02T15:04:05Z"))
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
		req := &gamejanitor.CreateBackupRequest{}
		if name != "" {
			req.Name = name
		}

		if !jsonOutput {
			fmt.Println("Creating backup...")
		}

		err = getClient().Backups.Create(ctx(), gsID, req)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
			return nil
		}

		fmt.Println("Backup creation started.")
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

		err = getClient().Backups.Restore(ctx(), gsID, backupID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
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

		err = getClient().Backups.Delete(ctx(), gsID, backupID)
		if err != nil {
			return exitError(err)
		}

		if jsonOutput {
			printJSON(map[string]string{"status": "ok"})
			return nil
		}

		fmt.Printf("Backup %s deleted.\n", backupID[:8])
		return nil
	},
}

var backupsDownloadCmd = &cobra.Command{
	Use:   "download <gameserver> <backup> [file]",
	Short: "Download a backup to a local file",
	Args:  cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		gsID, err := resolveGameserverID(args[0])
		if err != nil {
			return exitError(err)
		}
		backupID, err := resolveBackupID(gsID, args[1])
		if err != nil {
			return exitError(err)
		}

		body, err := getClient().Backups.Download(ctx(), gsID, backupID)
		if err != nil {
			return exitError(err)
		}
		defer body.Close()

		outPath := backupID[:8] + ".tar.gz"
		if len(args) == 3 {
			outPath = args[2]
		}

		f, err := os.Create(outPath)
		if err != nil {
			return exitError(fmt.Errorf("creating file: %w", err))
		}
		defer f.Close()

		written, err := io.Copy(f, body)
		if err != nil {
			return exitError(fmt.Errorf("writing backup: %w", err))
		}

		fmt.Printf("Downloaded backup to %s (%s)\n", outPath, formatBytes(written))
		return nil
	},
}
