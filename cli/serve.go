package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"io/fs"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/db"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	gjsftp "github.com/warsmite/gamejanitor/sftp"
	"github.com/warsmite/gamejanitor/tlsutil"
	"github.com/warsmite/gamejanitor/api"
	"github.com/warsmite/gamejanitor/ui"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Gamejanitor HTTP server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().String("config", "", "Path to YAML config file")
	serveCmd.Flags().IntP("port", "p", 0, "Port to listen on")
	serveCmd.Flags().String("bind", "", "Bind address for all listeners")
	serveCmd.Flags().Int("sftp-port", 0, "SFTP server port (0 to disable)")
	serveCmd.Flags().Int("grpc-port", 0, "gRPC port (0 to disable)")
	serveCmd.Flags().Int("worker-grpc-port", 0, "Worker gRPC port for dial-back (controller+worker mode)")
	serveCmd.Flags().Bool("controller", false, "Enable controller role")
	serveCmd.Flags().Bool("worker", false, "Enable worker role")
	serveCmd.Flags().StringP("data-dir", "d", "", "Data directory for database and backups")
	serveCmd.Flags().String("controller-address", "", "Controller gRPC address for worker registration")
	serveCmd.Flags().String("worker-id", "", "Worker ID (defaults to hostname)")
	serveCmd.Flags().String("worker-token", "", "Worker auth token for gRPC registration")
	serveCmd.Flags().String("runtime", "", "Container runtime: docker, podman, process, auto")
}

// loadConfig loads config file (if any) and applies CLI flag overrides.
func loadConfig(cmd *cobra.Command) (config.Config, error) {
	configPath, _ := cmd.Flags().GetString("config")
	if configPath == "" {
		configPath = config.Discover()
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return cfg, err
	}

	// CLI flags override config file values (only if explicitly set)
	if cmd.Flags().Changed("bind") {
		cfg.Bind, _ = cmd.Flags().GetString("bind")
	}
	if cmd.Flags().Changed("port") {
		cfg.Port, _ = cmd.Flags().GetInt("port")
	}
	if cmd.Flags().Changed("grpc-port") {
		cfg.GRPCPort, _ = cmd.Flags().GetInt("grpc-port")
	}
	if cmd.Flags().Changed("worker-grpc-port") {
		cfg.WorkerGRPCPort, _ = cmd.Flags().GetInt("worker-grpc-port")
	}
	if cmd.Flags().Changed("sftp-port") {
		cfg.SFTPPort, _ = cmd.Flags().GetInt("sftp-port")
	}
	if cmd.Flags().Changed("controller") {
		cfg.Controller, _ = cmd.Flags().GetBool("controller")
	}
	if cmd.Flags().Changed("worker") {
		cfg.Worker, _ = cmd.Flags().GetBool("worker")
	}
	if cmd.Flags().Changed("data-dir") {
		cfg.DataDir, _ = cmd.Flags().GetString("data-dir")
		cfg.DBPath = filepath.Join(cfg.DataDir, "gamejanitor.db")
	}
	if cmd.Flags().Changed("controller-address") {
		cfg.ControllerAddress, _ = cmd.Flags().GetString("controller-address")
	}
	if cmd.Flags().Changed("worker-id") {
		cfg.WorkerID, _ = cmd.Flags().GetString("worker-id")
	}
	if cmd.Flags().Changed("worker-token") {
		cfg.WorkerToken, _ = cmd.Flags().GetString("worker-token")
	}
	if cmd.Flags().Changed("runtime") {
		cfg.ContainerRuntime, _ = cmd.Flags().GetString("runtime")
	}

	return cfg, nil
}

type services struct {
	broadcaster   *service.EventBus
	settingsSvc   *service.SettingsService
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	statsPoller   *service.StatsPoller
	readyWatcher  *service.ReadyWatcher
	consoleSvc    *service.ConsoleService
	fileSvc       *service.FileService
	backupSvc     *service.BackupService
	scheduler     *service.Scheduler
	scheduleSvc   *service.ScheduleService
	authSvc       *service.AuthService
	statusMgr     *service.StatusManager
	statusSub     *service.StatusSubscriber
	eventStore    *service.EventStoreSubscriber
	webhookWorker *service.WebhookWorker
	modSvc        *service.ModService
}

func initServices(database *sql.DB, dispatcher *worker.Dispatcher, registry *worker.Registry, gameStore *games.GameStore, cfg config.Config, logger *slog.Logger) (*services, error) {
	broadcaster := service.NewEventBus()
	settingsSvc := service.NewSettingsServiceWithMode(database, logger, cfg.Mode)

	// Apply config file runtime settings to DB on every startup
	settingsSvc.ApplyConfig(cfg.Settings)

	gameserverSvc := service.NewGameserverService(database, dispatcher, broadcaster, settingsSvc, gameStore, cfg.DataDir, logger)
	querySvc := service.NewQueryService(database, broadcaster, gameStore, logger)
	statsPoller := service.NewStatsPoller(database, dispatcher, broadcaster, logger)
	readyWatcher := service.NewReadyWatcher(database, broadcaster, gameStore, logger)
	readyWatcher.SetQueryService(querySvc)
	readyWatcher.SetStatsPoller(statsPoller)
	gameserverSvc.SetReadyWatcher(readyWatcher)
	consoleSvc := service.NewConsoleService(database, dispatcher, gameStore, logger)
	fileSvc := service.NewFileService(database, dispatcher, logger)

	backupStore, err := initBackupStore(cfg, logger)
	if err != nil {
		return nil, err
	}

	gameserverSvc.SetBackupStore(backupStore)
	backupSvc := service.NewBackupService(database, dispatcher, gameserverSvc, gameStore, backupStore, settingsSvc, broadcaster, logger)
	scheduler := service.NewScheduler(database, backupSvc, gameserverSvc, consoleSvc, broadcaster, logger)
	scheduleSvc := service.NewScheduleService(database, scheduler, broadcaster, logger)
	authSvc := service.NewAuthService(database, logger)
	statusMgr := service.NewStatusManager(database, broadcaster, querySvc, statsPoller, readyWatcher, dispatcher, registry, gameserverSvc.Start, logger)
	statusSub := service.NewStatusSubscriber(database, broadcaster, logger)
	eventStore := service.NewEventStoreSubscriber(database, broadcaster, logger)
	webhookWorker := service.NewWebhookWorker(database, broadcaster, logger)
	optionsRegistry := games.NewOptionsRegistry(logger)
	modSvc := service.NewModService(database, fileSvc, gameStore, settingsSvc, optionsRegistry, broadcaster, logger)

	return &services{
		broadcaster:   broadcaster,
		settingsSvc:   settingsSvc,
		gameserverSvc: gameserverSvc,
		querySvc:      querySvc,
		statsPoller:   statsPoller,
		readyWatcher:  readyWatcher,
		consoleSvc:    consoleSvc,
		fileSvc:       fileSvc,
		backupSvc:     backupSvc,
		scheduler:     scheduler,
		scheduleSvc:   scheduleSvc,
		authSvc:       authSvc,
		statusMgr:     statusMgr,
		statusSub:     statusSub,
		eventStore:    eventStore,
		webhookWorker: webhookWorker,
		modSvc:        modSvc,
	}, nil
}

func initBackupStore(cfg config.Config, logger *slog.Logger) (service.BackupStore, error) {
	bs := cfg.BackupStore
	if bs == nil || bs.Type == "" || bs.Type == "local" {
		logger.Info("backup store: local", "path", cfg.DataDir)
		return service.NewLocalStore(cfg.DataDir), nil
	}

	if bs.Type == "s3" {
		s3Store, err := service.NewS3Store(bs, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize backup store: %w", err)
		}
		return s3Store, nil
	}

	return nil, fmt.Errorf("unknown backup_store type: %q (must be \"local\" or \"s3\")", bs.Type)
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	if !cfg.HasController() && !cfg.HasWorker() {
		return fmt.Errorf("at least one of controller or worker must be enabled")
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

	logWriter := io.MultiWriter(os.Stderr, logFile)
	logger := slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	// Worker-only mode: start gRPC agent and exit (no DB, no web UI)
	if cfg.WorkerOnly() {
		return runWorkerAgent(cfg, logger)
	}

	role := "controller"
	if cfg.HasWorker() {
		role = "controller+worker"
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

	gameStore, err := games.NewGameStore(filepath.Join(cfg.DataDir, "games"), logger)
	if err != nil {
		return fmt.Errorf("failed to initialize game store: %w", err)
	}

	registry := worker.NewRegistry(logger)
	dispatcher := worker.NewDispatcher(registry, database, logger)

	svcs, err := initServices(database, dispatcher, registry, gameStore, cfg, logger)
	if err != nil {
		return err
	}

	ctx := context.Background()
	svcs.statusMgr.Start(ctx)
	defer svcs.statusMgr.Stop()

	svcs.statusSub.Start(ctx)
	defer svcs.statusSub.Stop()

	svcs.eventStore.Start(ctx)
	defer svcs.eventStore.Stop()

	svcs.webhookWorker.Start(ctx)
	defer svcs.webhookWorker.Stop()

	// Prune old events on startup, then hourly
	go func() {
		retDays := svcs.settingsSvc.GetInt(service.SettingEventRetention)
		if retDays > 0 {
			if pruned, err := models.PruneEvents(database, retDays); err != nil {
				logger.Error("failed to prune events on startup", "error", err)
			} else if pruned > 0 {
				logger.Info("pruned old events", "count", pruned, "retention_days", retDays)
			}
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			days := svcs.settingsSvc.GetInt(service.SettingEventRetention)
			if days > 0 {
				if pruned, err := models.PruneEvents(database, days); err != nil {
					logger.Error("failed to prune events", "error", err)
				} else if pruned > 0 {
					logger.Info("pruned old events", "count", pruned, "retention_days", days)
				}
			}
		}
	}()

	if err := svcs.scheduler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	defer svcs.scheduler.Stop()
	defer svcs.readyWatcher.StopAll()
	defer svcs.querySvc.StopAll()

	// Start gRPC server for controller
	var serverTLS, dialBackTLS *tls.Config
	var caCert *x509.Certificate
	var caKey *ecdsa.PrivateKey
	{
		var err error
		caCert, caKey, err = tlsutil.LoadOrCreateCA(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("failed to initialize gRPC CA: %w", err)
		}
		if _, err := tlsutil.LoadOrCreateServerCert(cfg.DataDir, caCert, caKey); err != nil {
			return fmt.Errorf("failed to initialize gRPC server cert: %w", err)
		}
		serverTLS, err = tlsutil.ServerTLSConfig(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("failed to load gRPC server TLS config: %w", err)
		}
		if serverTLS != nil {
			dialBackTLS = &tls.Config{
				Certificates: serverTLS.Certificates,
				RootCAs:      serverTLS.ClientCAs,
			}
		}
	}
	go func() {
		if err := startGRPCServer(nil, gameStore, cfg.DataDir, registry, svcs.authSvc, database, cfg.Bind, cfg.GRPCPort, serverTLS, dialBackTLS, caCert, caKey, logger); err != nil {
			logger.Error("grpc server stopped", "error", err)
		}
	}()

	// Launch local worker agent in controller+worker mode
	if cfg.HasWorker() {
		rawToken, _, err := svcs.authSvc.RotateWorkerToken("_local")
		if err != nil {
			return fmt.Errorf("failed to create local worker token: %w", err)
		}
		workerCfg := config.Config{
			Bind:              cfg.Bind,
			Controller:        false,
			Worker:            true,
			DataDir:           cfg.DataDir,
			GRPCPort:          cfg.WorkerGRPCPort,
			SFTPPort:          0,
			ControllerAddress: fmt.Sprintf("127.0.0.1:%d", cfg.GRPCPort),
			WorkerToken:       rawToken,
			ContainerRuntime:  cfg.ContainerRuntime,
			ContainerSocket:   cfg.ContainerSocket,
		}
		go func() {
			if err := runWorkerAgent(workerCfg, logger); err != nil {
				logger.Error("local worker agent failed", "error", err)
			}
		}()
	}

	registry.StartReaper(ctx, logger)

	router := api.NewRouter(api.RouterOptions{
		Config:        cfg,
		Role:          role,
		LogPath:       logPath,
		GameStore:     gameStore,
		GameserverSvc: svcs.gameserverSvc,
		ConsoleSvc:    svcs.consoleSvc,
		FileSvc:       svcs.fileSvc,
		ScheduleSvc:   svcs.scheduleSvc,
		BackupSvc:     svcs.backupSvc,
		QuerySvc:      svcs.querySvc,
		StatsPoller:   svcs.statsPoller,
		SettingsSvc:   svcs.settingsSvc,
		AuthSvc:       svcs.authSvc,
		Broadcaster:   svcs.broadcaster,
		Registry:      registry,
		DB:            database,
		ModSvc:        svcs.modSvc,
		Log:           logger,
		WebUI:         webUIFS(cfg),
	})

	// Start SFTP server if enabled
	if cfg.SFTPPort > 0 {
		hostKeyPath := filepath.Join(cfg.DataDir, "sftp_host_key")
		sftpAuth := gjsftp.NewLocalAuth(database)
		fileOpFactory := func(gameserverID string) gjsftp.FileOperator {
			return gjsftp.NewDispatcherFileOperator(dispatcher, gameserverID)
		}
		sftpServer, err := gjsftp.NewServer(sftpAuth, fileOpFactory, hostKeyPath, logger)
		if err != nil {
			return fmt.Errorf("failed to initialize sftp server: %w", err)
		}
		defer sftpServer.Close()

		go func() {
			sftpAddr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.SFTPPort)
			if err := sftpServer.ListenAndServe(sftpAddr); err != nil {
				logger.Error("sftp server stopped", "error", err)
			}
		}()
	}

	if !isLoopback(cfg.Bind) && !svcs.settingsSvc.GetBool(service.SettingAuthEnabled) {
		logger.Warn("listening on public address with auth disabled — anyone on your network can manage your gameservers",
			"bind_address", cfg.Bind, "port", cfg.Port)
	}

	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	logger.Info("starting gamejanitor",
		"role", role,
		"bind_address", cfg.Bind,
		"port", cfg.Port,
		"sftp_port", cfg.SFTPPort,
		"grpc_port", cfg.GRPCPort,
		"data_dir", cfg.DataDir,
		"db_path", cfg.DBPath,
	)

	// Newbies running from a terminal may not realize that closing it kills gamejanitor,
	// even though their gameservers keep running in Docker. Scheduled backups, restarts,
	// and status monitoring all stop when gamejanitor exits.
	if os.Getenv("INVOCATION_ID") == "" {
		logger.Warn("running in foreground — closing this terminal will stop scheduled backups, restarts, and status monitoring. Your gameservers will keep running, but gamejanitor won't be managing them. Run as a systemd service to keep it running in the background.")
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}

func webUIFS(cfg config.Config) fs.FS {
	if !cfg.WebUI {
		return nil
	}
	sub, err := fs.Sub(ui.Dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}
