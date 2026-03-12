package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/0xkowalskidev/gamejanitor/internal/config"
	"github.com/0xkowalskidev/gamejanitor/internal/db"
	"github.com/0xkowalskidev/gamejanitor/internal/db/seed"
	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/web"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Gamejanitor HTTP server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().IntP("port", "p", 8080, "Port to listen on")
	serveCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory for database and backups")
}

func runServe(cmd *cobra.Command, args []string) error {
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return fmt.Errorf("invalid port flag: %w", err)
	}
	dataDir, err := cmd.Flags().GetString("data-dir")
	if err != nil {
		return fmt.Errorf("invalid data-dir flag: %w", err)
	}

	cfg := config.Config{
		Port:    port,
		DataDir: dataDir,
		DBPath:  filepath.Join(dataDir, "gamejanitor.db"),
	}

	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	logger.Info("opening database", "path", cfg.DBPath)
	database, err := db.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer database.Close()

	logger.Info("running migrations")
	if err := db.Migrate(database); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	logger.Info("seeding game data")
	if err := seed.SeedGames(database); err != nil {
		return fmt.Errorf("failed to seed games: %w", err)
	}

	dockerClient, err := docker.New(logger)
	if err != nil {
		return fmt.Errorf("failed to connect to docker: %w", err)
	}
	defer dockerClient.Close()

	// Initialize services
	broadcaster := service.NewEventBroadcaster()
	gameSvc := service.NewGameService(database, logger)
	gameserverSvc := service.NewGameserverService(database, dockerClient, broadcaster, logger)
	consoleSvc := service.NewConsoleService(database, dockerClient, logger)
	statusMgr := service.NewStatusManager(database, dockerClient, broadcaster, logger)

	// Crash recovery
	ctx := context.Background()
	autoStartIDs, err := statusMgr.RecoverOnStartup(ctx)
	if err != nil {
		return fmt.Errorf("failed to recover gameserver status: %w", err)
	}

	// Start status manager (Docker events watcher)
	statusMgr.Start(ctx)
	defer statusMgr.Stop()

	// Auto-start gameservers
	for _, id := range autoStartIDs {
		logger.Info("auto-starting gameserver", "id", id)
		if err := gameserverSvc.Start(ctx, id); err != nil {
			logger.Error("failed to auto-start gameserver", "id", id, "error", err)
		}
	}

	router, err := web.NewRouter(gameSvc, gameserverSvc, consoleSvc, dockerClient, broadcaster, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	logger.Info("starting gamejanitor", "port", cfg.Port, "data_dir", cfg.DataDir, "db_path", cfg.DBPath)

	return http.ListenAndServe(addr, router)
}
