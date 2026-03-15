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
	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/netinfo"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/web"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
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

	dockerClient, err := docker.New(logger)
	if err != nil {
		return fmt.Errorf("failed to connect to docker: %w", err)
	}
	defer dockerClient.Close()

	// Initialize game store
	gameStore, err := games.NewGameStore(filepath.Join(cfg.DataDir, "games"), logger)
	if err != nil {
		return fmt.Errorf("failed to initialize game store: %w", err)
	}

	// Initialize worker layer
	localWorker := worker.NewLocalWorker(dockerClient, logger)
	dispatcher := worker.NewLocalDispatcher(localWorker)

	// Initialize services
	broadcaster := service.NewEventBroadcaster()
	settingsSvc := service.NewSettingsService(database, logger)
	gameserverSvc := service.NewGameserverService(database, dispatcher, broadcaster, settingsSvc, gameStore, cfg.DataDir, logger)
	querySvc := service.NewQueryService(database, broadcaster, gameStore, logger)
	gameserverSvc.SetQueryService(querySvc)
	consoleSvc := service.NewConsoleService(database, dispatcher, gameStore, logger)
	fileSvc := service.NewFileService(database, dispatcher, logger)
	backupSvc := service.NewBackupService(database, dispatcher, gameserverSvc, gameStore, cfg.DataDir, logger)
	scheduler := service.NewScheduler(database, backupSvc, gameserverSvc, consoleSvc, logger)
	scheduleSvc := service.NewScheduleService(database, scheduler, logger)
	statusMgr := service.NewStatusManager(database, localWorker, broadcaster, querySvc, logger)

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

	netInfo := netinfo.Detect(logger)

	router, err := web.NewRouter(gameStore, gameserverSvc, consoleSvc, fileSvc, scheduleSvc, backupSvc, querySvc, settingsSvc, broadcaster, netInfo, logPath, cfg.DataDir, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	addr := fmt.Sprintf(":%d", cfg.Port)
	logger.Info("starting gamejanitor", "port", cfg.Port, "data_dir", cfg.DataDir, "db_path", cfg.DBPath)

	return http.ListenAndServe(addr, router)
}
