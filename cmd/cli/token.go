package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/0xkowalskidev/gamejanitor/internal/db"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage auth tokens offline (direct DB access, no running server needed)",
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new auth token",
	RunE:  runTokenCreate,
}

func init() {
	tokenCreateCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory containing the database")
	tokenCreateCmd.Flags().String("name", "", "Token name (required)")
	tokenCreateCmd.Flags().String("type", "worker", "Token type: worker, admin")
	tokenCreateCmd.MarkFlagRequired("name")

	tokenCmd.AddCommand(tokenCreateCmd)
	tokenCmd.GroupID = "server"
}

func runTokenCreate(cmd *cobra.Command, args []string) error {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	name, _ := cmd.Flags().GetString("name")
	tokenType, _ := cmd.Flags().GetString("type")

	if tokenType != "worker" && tokenType != "admin" {
		return fmt.Errorf("token type must be 'worker' or 'admin', got %q", tokenType)
	}

	dbPath := filepath.Join(dataDir, "gamejanitor.db")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("creating data directory: %w", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening database at %s: %w", dbPath, err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc := service.NewAuthService(database, logger)

	switch tokenType {
	case "worker":
		rawToken, _, err := authSvc.CreateWorkerToken(name)
		if err != nil {
			return fmt.Errorf("creating worker token: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Worker token created. Store this — it cannot be retrieved later.")
		// Raw token on stdout for piping into secret managers
		fmt.Println(rawToken)

	case "admin":
		rawToken, err := authSvc.GenerateAdminToken()
		if err != nil {
			return fmt.Errorf("creating admin token: %w", err)
		}
		fmt.Fprintln(os.Stderr, "Admin token created (replaces any existing admin token). Store this — it cannot be retrieved later.")
		fmt.Println(rawToken)
	}

	return nil
}
