package cli

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/db"
	"github.com/warsmite/gamejanitor/store"
	"github.com/spf13/cobra"
)

var tokensOfflineCmd = &cobra.Command{
	Use:   "offline",
	Short: "Manage tokens directly via database (no running server needed)",
}

func init() {
	tokensOfflineCreateCmd.Flags().String("config", "", "Path to config file")
	tokensOfflineCreateCmd.Flags().StringP("data-dir", "d", "", "Data directory (overrides config)")
	tokensOfflineCreateCmd.Flags().String("name", "", "Token name (required)")
	tokensOfflineCreateCmd.Flags().String("type", "worker", "Token type: worker, admin")
	tokensOfflineCreateCmd.MarkFlagRequired("name")

	tokensOfflineRotateCmd.Flags().String("config", "", "Path to config file")
	tokensOfflineRotateCmd.Flags().StringP("data-dir", "d", "", "Data directory (overrides config)")
	tokensOfflineRotateCmd.Flags().String("name", "", "Token name (required)")
	tokensOfflineRotateCmd.Flags().String("type", "worker", "Token type: worker, admin")
	tokensOfflineRotateCmd.MarkFlagRequired("name")

	tokensOfflineCmd.AddCommand(tokensOfflineCreateCmd, tokensOfflineRotateCmd)
	tokensCmd.AddCommand(tokensOfflineCmd)
}

var tokensOfflineCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a token via direct DB access",
	RunE:  runTokenOfflineCreate,
}

var tokensOfflineRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Rotate a token via direct DB access",
	RunE:  runTokenOfflineRotate,
}

func openAuthService(cmd *cobra.Command) (*auth.AuthService, func(), error) {
	// Resolve data dir: explicit --data-dir > config file > default
	dataDir, _ := cmd.Flags().GetString("data-dir")
	if dataDir == "" {
		configPath, _ := cmd.Flags().GetString("config")
		if configPath == "" {
			configPath = config.Discover()
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, nil, fmt.Errorf("loading config: %w", err)
		}
		dataDir = cfg.DataDir
	}
	dbPath := filepath.Join(dataDir, "gamejanitor.db")
	slog.Info("using database", "path", dbPath)

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
	authStore := struct {
		*store.TokenStore
		*store.GameserverStore
	}{store.NewTokenStore(database), store.NewGameserverStore(database)}
	authSvc := auth.NewAuthService(authStore, logger)
	return authSvc, func() { database.Close() }, nil
}

func runTokenOfflineCreate(cmd *cobra.Command, args []string) error {
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
		fmt.Fprintf(os.Stderr, "Token %q already exists. Use 'gamejanitor tokens offline rotate' to generate a new secret.\n", name)
		return nil
	}

	fmt.Fprintln(os.Stderr, "Token created. Store this — it cannot be retrieved later.")
	fmt.Println(rawToken)
	return nil
}

func runTokenOfflineRotate(cmd *cobra.Command, args []string) error {
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
