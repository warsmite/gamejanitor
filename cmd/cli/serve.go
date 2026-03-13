package cli

import (
	"context"
	"fmt"
	"io"
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

	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	logPath := filepath.Join(cfg.DataDir, "gamejanitor.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer logFile.Close()

	writer := io.MultiWriter(os.Stderr, logFile)
	logger := slog.New(slog.NewJSONHandler(writer, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

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
	querySvc := service.NewQueryService(database, broadcaster, logger)
	gameserverSvc.SetQueryService(querySvc)
	consoleSvc := service.NewConsoleService(database, dockerClient, logger)
	fileSvc := service.NewFileService(database, dockerClient, logger)
	gameserverSvc.SetFileService(fileSvc)
	backupSvc := service.NewBackupService(database, dockerClient, gameserverSvc, cfg.DataDir, logger)
	scheduler := service.NewScheduler(database, backupSvc, gameserverSvc, consoleSvc, logger)
	scheduleSvc := service.NewScheduleService(database, scheduler, logger)
	statusMgr := service.NewStatusManager(database, dockerClient, broadcaster, querySvc, logger)

	// Crash recovery
	ctx := context.Background()
	if err := statusMgr.RecoverOnStartup(ctx); err != nil {
		return fmt.Errorf("failed to recover gameserver status: %w", err)
	}

	// Start status manager (Docker events watcher)
	statusMgr.Start(ctx)
	defer statusMgr.Stop()

	// Start scheduler
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	defer scheduler.Stop()
	defer querySvc.StopAll()
	defer fileSvc.CleanupAll()

	router, err := web.NewRouter(gameSvc, gameserverSvc, consoleSvc, fileSvc, scheduleSvc, backupSvc, querySvc, dockerClient, broadcaster, logPath, cfg.DataDir, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	logger.Info("starting gamejanitor", "port", cfg.Port, "data_dir", cfg.DataDir, "db_path", cfg.DBPath)

	return http.ListenAndServe(addr, router)
}
