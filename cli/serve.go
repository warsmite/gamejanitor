package cli

import (
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/db"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/pkg/netinfo"
	"github.com/warsmite/gamejanitor/controller/backup"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/schedule"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/controller/webhook"
	gjsftp "github.com/warsmite/gamejanitor/sftp"
	"github.com/warsmite/gamejanitor/pkg/tlsutil"
	"github.com/warsmite/gamejanitor/api"
	"github.com/warsmite/gamejanitor/ui"
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
	broadcaster   *controller.EventBus
	settingsSvc   *settings.SettingsService
	gameserverSvc *gameserver.GameserverService
	querySvc      *status.QueryService
	statsPoller   *status.StatsPoller
	readyWatcher  *status.ReadyWatcher
	consoleSvc    *gameserver.ConsoleService
	fileSvc       *gameserver.FileService
	backupSvc     *backup.BackupService
	scheduler     *schedule.Scheduler
	scheduleSvc   *schedule.ScheduleService
	authSvc       *auth.AuthService
	statusMgr     *status.StatusManager
	statusSub       *status.StatusSubscriber
	eventHistorySvc *event.EventHistoryService
	webhookWorker   *webhook.WebhookWorker
	webhookSvc      *webhook.WebhookEndpointService
	workerNodeSvc   *orchestrator.WorkerNodeService
	modSvc          *mod.ModService
}

func initServices(database *sql.DB, dispatcher *orchestrator.Dispatcher, registry *orchestrator.Registry, gameStore *games.GameStore, cfg config.Config, logger *slog.Logger) (*services, error) {
	broadcaster := controller.NewEventBus()
	db := store.New(database)

	settingsSvc := settings.NewSettingsServiceWithMode(db, logger, cfg.Mode)

	// Apply config file runtime settings to DB on every startup
	settingsSvc.ApplyConfig(cfg.Settings)

	gameserverSvc := gameserver.NewGameserverService(db, dispatcher, broadcaster, settingsSvc, gameStore, cfg.DataDir, logger)
	querySvc := status.NewQueryService(db, broadcaster, gameStore, logger)
	statsPoller := status.NewStatsPoller(db, dispatcher, broadcaster, logger)
	readyWatcher := status.NewReadyWatcher(db, broadcaster, gameStore, logger)
	gameserverSvc.SetReadyWatcher(readyWatcher)
	consoleSvc := gameserver.NewConsoleService(db, dispatcher, gameStore, logger)
	fileSvc := gameserver.NewFileService(db, dispatcher, logger)

	backupStorage, err := initBackupStorage(cfg, logger)
	if err != nil {
		return nil, err
	}

	// Activity tracking for long-running worker dispatches and CRUD events
	activityTracker := gameserver.NewActivityTracker(db, logger)
	gameserverSvc.SetActivityTracker(activityTracker)

	gameserverSvc.SetBackupStore(backupStorage)
	backupSvc := backup.NewBackupService(db, dispatcher, gameserverSvc, gameStore, backupStorage, settingsSvc, broadcaster, logger)
	backupSvc.SetActivityTracker(activityTracker)
	scheduler := schedule.NewScheduler(db, backupSvc, gameserverSvc, consoleSvc, broadcaster, logger)
	scheduleSvc := schedule.NewScheduleService(db, scheduler, broadcaster, logger)
	authSvc := auth.NewAuthService(db, logger)
	statusMgr := status.NewStatusManager(db, broadcaster, querySvc, statsPoller, readyWatcher, dispatcher, registry, gameserverSvc.Start, logger)
	statusSub := status.NewStatusSubscriber(db, broadcaster, querySvc, statsPoller, logger)
	eventHistorySvc := event.NewEventHistoryService(db)
	webhookWorker := webhook.NewWebhookWorker(db, db, broadcaster, logger)
	webhookSvc := webhook.NewWebhookEndpointService(db, logger)
	workerNodeSvc := orchestrator.NewWorkerNodeService(db, registry, broadcaster, logger)
	optionsRegistry := games.NewOptionsRegistry(logger)
	modSvc := mod.NewModService(db, fileSvc, gameStore, settingsSvc, optionsRegistry, broadcaster, logger)

	return &services{
		broadcaster:     broadcaster,
		settingsSvc:     settingsSvc,
		gameserverSvc:   gameserverSvc,
		querySvc:        querySvc,
		statsPoller:     statsPoller,
		readyWatcher:    readyWatcher,
		consoleSvc:      consoleSvc,
		fileSvc:         fileSvc,
		backupSvc:       backupSvc,
		scheduler:       scheduler,
		scheduleSvc:     scheduleSvc,
		authSvc:         authSvc,
		statusMgr:       statusMgr,
		statusSub:       statusSub,
		eventHistorySvc: eventHistorySvc,
		webhookWorker:   webhookWorker,
		webhookSvc:      webhookSvc,
		workerNodeSvc:   workerNodeSvc,
		modSvc:          modSvc,
	}, nil
}

func initBackupStorage(cfg config.Config, logger *slog.Logger) (backup.Storage, error) {
	bs := cfg.BackupStore
	if bs == nil || bs.Type == "" || bs.Type == "local" {
		logger.Info("backup store: local", "path", cfg.DataDir)
		return backup.NewLocalStorage(cfg.DataDir), nil
	}

	if bs.Type == "s3" {
		s3Storage, err := backup.NewS3Storage(bs, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize backup store: %w", err)
		}
		return s3Storage, nil
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

	// Log file always gets JSON (for log aggregation)
	fileHandler := slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: level})

	// Terminal gets colored human-readable output; pipes/systemd get JSON
	var stderrHandler slog.Handler
	if isTTY() && os.Getenv("NO_COLOR") == "" {
		stderrHandler = &colorLogHandler{level: level, w: os.Stderr}
	} else if isTTY() {
		stderrHandler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	} else {
		stderrHandler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	}

	logger := slog.New(multiHandler{stderrHandler, fileHandler})
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

	db := store.New(database)
	registry := orchestrator.NewRegistry(db, logger)
	if err := registry.LoadFromDB(); err != nil {
		return fmt.Errorf("failed to load workers from database: %w", err)
	}
	dispatcher := orchestrator.NewDispatcher(registry, db, logger)

	svcs, err := initServices(database, dispatcher, registry, gameStore, cfg, logger)
	if err != nil {
		return err
	}

	ctx := context.Background()
	svcs.statusMgr.Start(ctx)
	defer svcs.statusMgr.Stop()

	svcs.statusSub.Start(ctx)
	defer svcs.statusSub.Stop()

	svcs.webhookWorker.Start(ctx)
	defer svcs.webhookWorker.Stop()

	// Prune old activities on startup, then hourly
	go func() {
		retDays := svcs.settingsSvc.GetInt(settings.SettingEventRetention)
		if retDays > 0 {
			if pruned, err := db.PruneActivities(retDays); err != nil {
				logger.Error("failed to prune activities on startup", "error", err)
			} else if pruned > 0 {
				logger.Info("pruned old activities", "count", pruned, "retention_days", retDays)
			}
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			days := svcs.settingsSvc.GetInt(settings.SettingEventRetention)
			if days > 0 {
				if pruned, err := db.PruneActivities(days); err != nil {
					logger.Error("failed to prune activities", "error", err)
				} else if pruned > 0 {
					logger.Info("pruned old activities", "count", pruned, "retention_days", days)
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
		if err := startGRPCServer(nil, gameStore, cfg.DataDir, registry, svcs.authSvc, db, cfg.Bind, cfg.GRPCPort, serverTLS, dialBackTLS, caCert, caKey, logger); err != nil {
			logger.Error("grpc server stopped", "error", err)
		}
	}()

	// Launch local worker agent in controller+worker mode
	if cfg.HasWorker() {
		rawToken, _, err := svcs.authSvc.RotateWorkerToken("_local")
		if err != nil {
			return fmt.Errorf("failed to create local worker token: %w", err)
		}

		// When bound to a wildcard address, advertise the detected LAN IP
		// so the controller can dial back with a valid address and matching TLS SAN.
		advertiseHost := cfg.Bind
		if advertiseHost == "0.0.0.0" || advertiseHost == "::" || advertiseHost == "" {
			netInfo := netinfo.Detect(logger)
			if netInfo.LANIP != "" {
				advertiseHost = netInfo.LANIP
			} else {
				advertiseHost = "127.0.0.1"
			}
		}

		// Generate TLS cert for the local worker directly from the in-memory CA.
		// This avoids the enrollment RPC round-trip and eliminates stale cert issues
		// when the CA is regenerated — the cert always matches the current CA.
		if caCert != nil && caKey != nil {
			var workerIPs []net.IP
			if ip := net.ParseIP(advertiseHost); ip != nil {
				workerIPs = append(workerIPs, ip)
			}
			if err := generateLocalWorkerCert(cfg.DataDir, caCert, caKey, workerIPs, logger); err != nil {
				return fmt.Errorf("failed to generate local worker TLS cert: %w", err)
			}
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
			AdvertiseAddress:  fmt.Sprintf("%s:%d", advertiseHost, cfg.WorkerGRPCPort),
		}
		go func() {
			if err := runWorkerAgent(workerCfg, logger); err != nil {
				logger.Error("local worker agent failed", "error", err)
			}
		}()
	}

	registry.StartReaper(ctx, logger)

	// Reconcile gameserver status with Docker reality on startup.
	// Online workers get checked immediately; offline workers' gameservers
	// are marked unreachable and recovered when the worker reconnects.
	if err := svcs.statusMgr.RecoverOnStartup(ctx); err != nil {
		logger.Error("failed to recover gameserver status on startup", "error", err)
	}

	// Mark any activities still "running" from a previous crash as abandoned.
	if abandoned, err := db.AbandonRunningActivities(); err != nil {
		logger.Error("failed to abandon stale activities", "error", err)
	} else if abandoned > 0 {
		logger.Warn("abandoned stale activities from previous run", "count", abandoned)
	}

	router := api.NewRouter(api.RouterOptions{
		Config:          cfg,
		Role:            role,
		LogPath:         logPath,
		GameStore:       gameStore,
		GameserverSvc:   svcs.gameserverSvc,
		ConsoleSvc:      svcs.consoleSvc,
		FileSvc:         svcs.fileSvc,
		ScheduleSvc:     svcs.scheduleSvc,
		BackupSvc:       svcs.backupSvc,
		QuerySvc:        svcs.querySvc,
		StatsPoller:     svcs.statsPoller,
		SettingsSvc:     svcs.settingsSvc,
		AuthSvc:         svcs.authSvc,
		WorkerNodeSvc:   svcs.workerNodeSvc,
		WebhookSvc:      svcs.webhookSvc,
		EventHistorySvc: svcs.eventHistorySvc,
		ActivityStore:   db,
		Broadcaster:     svcs.broadcaster,
		ModSvc:          svcs.modSvc,
		Log:             logger,
		WebUI:           webUIFS(cfg),
	})

	// Start SFTP server if enabled
	if cfg.SFTPPort > 0 {
		hostKeyPath := filepath.Join(cfg.DataDir, "sftp_host_key")
		sftpAuth := gjsftp.NewLocalAuth(db)
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
				logger.Error("sftp server stopped", "error", listenError("sftp", sftpAddr, cfg.SFTPPort, err))
			}
		}()
	}

	if !isLoopback(cfg.Bind) && !svcs.settingsSvc.GetBool(settings.SettingAuthEnabled) {
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

	// Auto-open browser for interactive users (not systemd, has a TTY)
	if os.Getenv("INVOCATION_ID") == "" && isTTY() {
		go func() {
			// Small delay to let the server start accepting connections
			time.Sleep(500 * time.Millisecond)
			url := fmt.Sprintf("http://localhost:%d", cfg.Port)
			openBrowser(url)
		}()
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		return listenError("http", addr, cfg.Port, err)
	}
	return nil
}

func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}

// listenError wraps a listen error with a user-friendly message when the port is already in use.
func listenError(service, addr string, port int, err error) error {
	if strings.Contains(err.Error(), "address already in use") {
		return fmt.Errorf("%s server failed to start: port %d is already in use — another instance of gamejanitor or another program is using this port", service, port)
	}
	return fmt.Errorf("%s server failed to start on %s: %w", service, addr, err)
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// colorLogHandler writes colored, human-readable log lines to a terminal.
// Format: HH:MM:SS LEVEL msg key=value key=value
type colorLogHandler struct {
	level slog.Level
	w     *os.File
	attrs []slog.Attr
	group string
}

func (h *colorLogHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level
}

func (h *colorLogHandler) Handle(_ context.Context, r slog.Record) error {
	// Time — dimmed
	ts := r.Time.Format("15:04:05")

	// Level — colored
	var lvl string
	switch {
	case r.Level >= slog.LevelError:
		lvl = "\033[31mERR\033[0m" // red
	case r.Level >= slog.LevelWarn:
		lvl = "\033[33mWRN\033[0m" // yellow
	case r.Level >= slog.LevelInfo:
		lvl = "\033[36mINF\033[0m" // cyan
	default:
		lvl = "\033[90mDBG\033[0m" // gray
	}

	// Message — bright
	msg := r.Message

	// Attrs — dimmed
	var attrs string
	collect := func(a slog.Attr) bool {
		if a.Key != "" {
			attrs += fmt.Sprintf(" \033[90m%s=\033[0m%s", a.Key, a.Value.String())
		}
		return true
	}
	for _, a := range h.attrs {
		collect(a)
	}
	r.Attrs(collect)

	fmt.Fprintf(h.w, "\033[90m%s\033[0m %s %s%s\n", ts, lvl, msg, attrs)
	return nil
}

func (h *colorLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &colorLogHandler{level: h.level, w: h.w, attrs: append(h.attrs, attrs...), group: h.group}
}

func (h *colorLogHandler) WithGroup(name string) slog.Handler {
	return &colorLogHandler{level: h.level, w: h.w, attrs: h.attrs, group: name}
}

// multiHandler fans out log records to multiple handlers.
type multiHandler []slog.Handler

func (m multiHandler) Enabled(_ context.Context, level slog.Level) bool {
	for _, h := range m {
		if h.Enabled(context.Background(), level) {
			return true
		}
	}
	return false
}

func (m multiHandler) Handle(ctx context.Context, r slog.Record) error {
	for _, h := range m {
		if h.Enabled(ctx, r.Level) {
			if err := h.Handle(ctx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithAttrs(attrs)
	}
	return handlers
}

func (m multiHandler) WithGroup(name string) slog.Handler {
	handlers := make(multiHandler, len(m))
	for i, h := range m {
		handlers[i] = h.WithGroup(name)
	}
	return handlers
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return
	}
	cmd.Start()
}

// generateLocalWorkerCert generates a TLS cert for the local worker from the
// controller's in-memory CA and writes it to {dataDir}/certs/. This is picked
// up by loadWorkerTLS via auto-discovery, so the local worker skips enrollment.
// Regenerated every startup to guarantee the cert matches the current CA.
func generateLocalWorkerCert(dataDir string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, workerIPs []net.IP, logger *slog.Logger) error {
	caPEM, certPEM, keyPEM, err := tlsutil.GenerateWorkerCertPEM("_local", caCert, caKey, workerIPs)
	if err != nil {
		return fmt.Errorf("generating cert: %w", err)
	}

	certsDir := filepath.Join(dataDir, "certs")
	if err := os.MkdirAll(certsDir, 0700); err != nil {
		return fmt.Errorf("creating certs directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "ca.pem"), caPEM, 0644); err != nil {
		return fmt.Errorf("writing CA cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), certPEM, 0644); err != nil {
		return fmt.Errorf("writing cert: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "key.pem"), keyPEM, 0600); err != nil {
		return fmt.Errorf("writing key: %w", err)
	}

	logger.Info("generated local worker TLS cert from controller CA")
	return nil
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
