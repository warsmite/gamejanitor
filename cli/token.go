package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/db"
	"github.com/warsmite/gamejanitor/service"
	"github.com/spf13/cobra"
)

var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage auth tokens offline (direct DB access, no running server needed)",
}

var tokenCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new auth token (idempotent — returns existing if name already taken)",
	RunE:  runTokenCreate,
}

var tokenRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate an existing token (deletes old, creates new)",
	RunE:  runTokenRotate,
}

func init() {
	tokenCreateCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory containing the database")
	tokenCreateCmd.Flags().String("name", "", "Token name (required)")
	tokenCreateCmd.Flags().String("type", "worker", "Token type: worker, admin")
	tokenCreateCmd.MarkFlagRequired("name")

	tokenRotateCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory containing the database")
	tokenRotateCmd.Flags().String("name", "", "Token name (required)")
	tokenRotateCmd.Flags().String("type", "worker", "Token type: worker, admin")
	tokenRotateCmd.MarkFlagRequired("name")

	tokenCmd.AddCommand(tokenCreateCmd, tokenRotateCmd)
	tokenCmd.GroupID = "server"
}

func openAuthService(cmd *cobra.Command) (*service.AuthService, func(), error) {
	dataDir, _ := cmd.Flags().GetString("data-dir")
	dbPath := filepath.Join(dataDir, "gamejanitor.db")

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, nil, fmt.Errorf("creating data directory: %w", err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("opening database at %s: %w", dbPath, err)
	}

	if err := db.Migrate(database); err != nil {
		database.Close()
		return nil, nil, fmt.Errorf("running migrations: %w", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc := service.NewAuthService(database, logger)
	return authSvc, func() { database.Close() }, nil
}

func runTokenCreate(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	tokenType, _ := cmd.Flags().GetString("type")

	if tokenType != "worker" && tokenType != "admin" {
		return fmt.Errorf("token type must be 'worker' or 'admin', got %q", tokenType)
	}

	authSvc, cleanup, err := openAuthService(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	var rawToken string
	switch tokenType {
	case "worker":
		rawToken, _, err = authSvc.CreateWorkerToken(name)
	case "admin":
		rawToken, _, err = authSvc.CreateAdminToken(name)
	}
	if err != nil {
		return fmt.Errorf("creating %s token: %w", tokenType, err)
	}

	if rawToken == "" {
		fmt.Fprintf(os.Stderr, "Token %q already exists. Use 'gamejanitor token rotate' to generate a new secret.\n", name)
		return nil
	}

	fmt.Fprintln(os.Stderr, "Token created. Store this — it cannot be retrieved later.")
	fmt.Println(rawToken)
	return nil
}

func runTokenRotate(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	tokenType, _ := cmd.Flags().GetString("type")

	if tokenType != "worker" && tokenType != "admin" {
		return fmt.Errorf("token type must be 'worker' or 'admin', got %q", tokenType)
	}

	authSvc, cleanup, err := openAuthService(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	var rawToken string
	switch tokenType {
	case "worker":
		rawToken, _, err = authSvc.RotateWorkerToken(name)
	case "admin":
		rawToken, _, err = authSvc.RotateAdminToken(name)
	}
	if err != nil {
		return fmt.Errorf("rotating %s token: %w", tokenType, err)
	}

	fmt.Fprintln(os.Stderr, "Token rotated. Store this — it cannot be retrieved later.")
	fmt.Println(rawToken)
	return nil
}
