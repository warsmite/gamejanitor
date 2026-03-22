package cli

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/warsmite/gamejanitor/internal/config"
	"github.com/warsmite/gamejanitor/internal/db"
	"github.com/warsmite/gamejanitor/internal/docker"
	"github.com/warsmite/gamejanitor/internal/games"
	"github.com/warsmite/gamejanitor/internal/models"
	"github.com/warsmite/gamejanitor/internal/netinfo"
	"github.com/warsmite/gamejanitor/internal/service"
	gjsftp "github.com/warsmite/gamejanitor/internal/sftp"
	"github.com/warsmite/gamejanitor/internal/tlsutil"
	"github.com/warsmite/gamejanitor/internal/web"
	"github.com/warsmite/gamejanitor/internal/worker"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Gamejanitor HTTP server",
	RunE:  runServe,
}

func init() {
	serveCmd.Flags().IntP("port", "p", 8080, "Port to listen on")
	serveCmd.Flags().String("bind", "127.0.0.1", "Bind address for all listeners (or GJ_BIND env)")
	serveCmd.Flags().Int("sftp-port", 0, "SFTP server port (0 to disable)")
	serveCmd.Flags().Int("grpc-port", 0, "gRPC agent port for worker mode (0 to disable)")
	serveCmd.Flags().String("role", "standalone", "Server role: standalone, controller, worker, controller+worker")
	serveCmd.Flags().StringP("data-dir", "d", "/var/lib/gamejanitor", "Data directory for database and backups")
	serveCmd.Flags().String("controller", "", "Controller gRPC address for worker registration (e.g. 192.168.1.10:9090)")
	serveCmd.Flags().String("worker-id", "", "Worker ID (defaults to hostname)")
	serveCmd.Flags().String("worker-token", "", "Worker auth token for gRPC registration (or GJ_WORKER_TOKEN env)")
}

type services struct {
	broadcaster   *service.EventBus
	settingsSvc   *service.SettingsService
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	readyWatcher  *service.ReadyWatcher
	consoleSvc    *service.ConsoleService
	fileSvc       *service.FileService
	backupSvc     *service.BackupService
	scheduler     *service.Scheduler
	scheduleSvc   *service.ScheduleService
	authSvc        *service.AuthService
	statusMgr      *service.StatusManager
	statusSub      *service.StatusSubscriber
	eventStore     *service.EventStoreSubscriber
	webhookWorker  *service.WebhookWorker
}

func initServices(database *sql.DB, dispatcher *worker.Dispatcher, localWorker worker.Worker, registry *worker.Registry, gameStore *games.GameStore, cfg config.Config, logger *slog.Logger) (*services, error) {
	broadcaster := service.NewEventBus()
	settingsSvc := service.NewSettingsService(database, logger)
	gameserverSvc := service.NewGameserverService(database, dispatcher, broadcaster, settingsSvc, gameStore, nil, cfg.DataDir, logger)
	querySvc := service.NewQueryService(database, broadcaster, gameStore, logger)
	readyWatcher := service.NewReadyWatcher(database, broadcaster, gameStore, logger)
	readyWatcher.SetQueryService(querySvc)
	gameserverSvc.SetReadyWatcher(readyWatcher)
	consoleSvc := service.NewConsoleService(database, dispatcher, gameStore, logger)
	fileSvc := service.NewFileService(database, dispatcher, logger)

	var backupStore service.BackupStore
	if bucket := os.Getenv("GJ_S3_BUCKET"); bucket != "" {
		s3Store, err := service.NewS3Store(
			os.Getenv("GJ_S3_ENDPOINT"),
			bucket,
			os.Getenv("GJ_S3_REGION"),
			os.Getenv("GJ_S3_ACCESS_KEY"),
			os.Getenv("GJ_S3_SECRET_KEY"),
			os.Getenv("GJ_S3_PATH_STYLE") == "true",
			os.Getenv("GJ_S3_USE_SSL") != "false",
			logger,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize S3 backup store: %w", err)
		}
		backupStore = s3Store
	} else {
		backupStore = service.NewLocalStore(cfg.DataDir)
		logger.Info("backup store: local", "path", cfg.DataDir)
	}

	gameserverSvc.SetBackupStore(backupStore)
	backupSvc := service.NewBackupService(database, dispatcher, gameserverSvc, gameStore, backupStore, settingsSvc, broadcaster, logger)
	scheduler := service.NewScheduler(database, backupSvc, gameserverSvc, consoleSvc, broadcaster, logger)
	scheduleSvc := service.NewScheduleService(database, scheduler, logger)
	authSvc := service.NewAuthService(database, logger)
	statusMgr := service.NewStatusManager(database, localWorker, broadcaster, querySvc, readyWatcher, dispatcher, registry, gameserverSvc.Start, logger)
	statusSub := service.NewStatusSubscriber(database, broadcaster, logger)
	eventStore := service.NewEventStoreSubscriber(database, broadcaster, logger)
	webhookWorker := service.NewWebhookWorker(database, broadcaster, logger)

	return &services{
		broadcaster:   broadcaster,
		settingsSvc:   settingsSvc,
		gameserverSvc: gameserverSvc,
		querySvc:      querySvc,
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
	}, nil
}

func runServe(cmd *cobra.Command, args []string) error {
	port, err := cmd.Flags().GetInt("port")
	if err != nil {
		return fmt.Errorf("invalid port flag: %w", err)
	}
	sftpPort, err := cmd.Flags().GetInt("sftp-port")
	if err != nil {
		return fmt.Errorf("invalid sftp-port flag: %w", err)
	}
	grpcPort, err := cmd.Flags().GetInt("grpc-port")
	if err != nil {
		return fmt.Errorf("invalid grpc-port flag: %w", err)
	}
	role, err := cmd.Flags().GetString("role")
	if err != nil {
		return fmt.Errorf("invalid role flag: %w", err)
	}
	dataDir, err := cmd.Flags().GetString("data-dir")
	if err != nil {
		return fmt.Errorf("invalid data-dir flag: %w", err)
	}
	controllerAddr, err := cmd.Flags().GetString("controller")
	if err != nil {
		return fmt.Errorf("invalid controller flag: %w", err)
	}
	workerID, err := cmd.Flags().GetString("worker-id")
	if err != nil {
		return fmt.Errorf("invalid worker-id flag: %w", err)
	}
	workerToken, err := cmd.Flags().GetString("worker-token")
	if err != nil {
		return fmt.Errorf("invalid worker-token flag: %w", err)
	}
	if workerToken == "" {
		workerToken = os.Getenv("GJ_WORKER_TOKEN")
	}
	bindAddress, err := cmd.Flags().GetString("bind")
	if err != nil {
		return fmt.Errorf("invalid bind flag: %w", err)
	}
	if v := os.Getenv("GJ_BIND"); v != "" {
		bindAddress = v
	}

	hasLocalWorker := role == "standalone" || role == "worker" || role == "controller+worker"
	hasController := role == "standalone" || role == "controller" || role == "controller+worker"

	if !hasLocalWorker && !hasController {
		return fmt.Errorf("invalid role %q: must be standalone, controller, worker, or controller+worker", role)
	}

	cfg := config.Config{
		Port:        port,
		BindAddress: bindAddress,
		DataDir:     dataDir,
		DBPath:      filepath.Join(dataDir, "gamejanitor.db"),
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
	if role == "worker" {
		return runWorkerAgent(cfg, grpcPort, controllerAddr, workerID, workerToken, logger)
	}

	// Controller and standalone modes need a database
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

	// Initialize local worker if this node runs containers
	var localWorker worker.Worker
	if hasLocalWorker {
		dockerClient, err := docker.New(logger)
		if err != nil {
			return fmt.Errorf("failed to connect to docker: %w", err)
		}
		defer dockerClient.Close()
		localWorker = worker.NewLocalWorker(dockerClient, gameStore, cfg.DataDir, logger)
	}

	var dispatcher *worker.Dispatcher
	var registry *worker.Registry
	if role == "standalone" {
		dispatcher = worker.NewLocalDispatcher(localWorker)
	} else {
		registry = worker.NewRegistry(logger)
		dispatcher = worker.NewMultiNodeDispatcher(localWorker, registry, database, logger)
	}

	svcs, err := initServices(database, dispatcher, localWorker, registry, gameStore, cfg, logger)
	if err != nil {
		return err
	}

	ctx := context.Background()
	if localWorker != nil {
		if err := svcs.statusMgr.RecoverOnStartup(ctx); err != nil {
			return fmt.Errorf("failed to recover gameserver status: %w", err)
		}
		svcs.statusMgr.Start(ctx)
	}
	defer svcs.statusMgr.Stop()

	svcs.statusSub.Start(ctx)
	defer svcs.statusSub.Stop()

	svcs.eventStore.Start(ctx)
	defer svcs.eventStore.Stop()

	svcs.webhookWorker.Start(ctx)
	defer svcs.webhookWorker.Stop()

	// Prune old events on startup, then hourly
	go func() {
		retDays := svcs.settingsSvc.GetEventRetentionDays()
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
			days := svcs.settingsSvc.GetEventRetentionDays()
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

	// Start gRPC server for controller and/or local worker agent
	if grpcPort > 0 {
		var serverTLS, dialBackTLS *tls.Config
		if role != "standalone" {
			caCert, caKey, err := tlsutil.LoadOrCreateCA(cfg.DataDir)
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
			if err := startGRPCServer(localWorker, gameStore, cfg.DataDir, registry, svcs.authSvc, database, cfg.BindAddress, grpcPort, serverTLS, dialBackTLS, logger); err != nil {
				logger.Error("grpc server stopped", "error", err)
			}
		}()
	}

	if registry != nil {
		registry.StartReaper(ctx, logger)
	}

	netInfo := netinfo.Detect(logger)

	router, err := web.NewRouter(gameStore, svcs.gameserverSvc, svcs.consoleSvc, svcs.fileSvc, svcs.scheduleSvc, svcs.backupSvc, svcs.querySvc, svcs.settingsSvc, svcs.authSvc, svcs.broadcaster, netInfo, registry, database, logPath, cfg.DataDir, cfg.BindAddress, cfg.Port, sftpPort, role, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	// Start SFTP server if enabled
	if sftpPort > 0 {
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
			sftpAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, sftpPort)
			if err := sftpServer.ListenAndServe(sftpAddr); err != nil {
				logger.Error("sftp server stopped", "error", err)
			}
		}()
	}

	if !isLoopback(cfg.BindAddress) && !svcs.settingsSvc.GetAuthEnabled() {
		logger.Warn("listening on public address with auth disabled — anyone on your network can manage your gameservers",
			"bind_address", cfg.BindAddress, "port", cfg.Port)
	}

	addr := fmt.Sprintf("%s:%d", cfg.BindAddress, cfg.Port)
	logger.Info("starting gamejanitor",
		"role", role,
		"bind_address", cfg.BindAddress,
		"port", cfg.Port,
		"sftp_port", sftpPort,
		"grpc_port", grpcPort,
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
