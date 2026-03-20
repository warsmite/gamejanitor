package cli

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/config"
	"github.com/0xkowalskidev/gamejanitor/internal/db"
	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/netinfo"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	gjsftp "github.com/0xkowalskidev/gamejanitor/internal/sftp"
	"github.com/0xkowalskidev/gamejanitor/internal/tlsutil"
	"github.com/0xkowalskidev/gamejanitor/internal/web"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/0xkowalskidev/gamejanitor/internal/worker/pb"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	grpcCredentials "google.golang.org/grpc/credentials"
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

	// Initialize game store
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

	// Initialize dispatcher
	var dispatcher *worker.Dispatcher
	var registry *worker.Registry
	if role == "standalone" {
		dispatcher = worker.NewLocalDispatcher(localWorker)
	} else {
		registry = worker.NewRegistry(logger)
		dispatcher = worker.NewMultiNodeDispatcher(localWorker, registry, database, logger)
	}

	// Initialize services
	broadcaster := service.NewEventBroadcaster()
	settingsSvc := service.NewSettingsService(database, logger)
	gameserverSvc := service.NewGameserverService(database, dispatcher, broadcaster, settingsSvc, gameStore, cfg.DataDir, logger)
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
			return fmt.Errorf("failed to initialize S3 backup store: %w", err)
		}
		backupStore = s3Store
	} else {
		backupStore = service.NewLocalStore(cfg.DataDir)
		logger.Info("backup store: local", "path", cfg.DataDir)
	}
	backupSvc := service.NewBackupService(database, dispatcher, gameserverSvc, gameStore, backupStore, settingsSvc, logger)
	scheduler := service.NewScheduler(database, backupSvc, gameserverSvc, consoleSvc, logger)
	scheduleSvc := service.NewScheduleService(database, scheduler, logger)
	authSvc := service.NewAuthService(database, logger)

	// Status manager — watches Docker events for status updates
	statusMgr := service.NewStatusManager(database, localWorker, broadcaster, querySvc, readyWatcher, dispatcher, registry, logger)

	ctx := context.Background()
	if localWorker != nil {
		if err := statusMgr.RecoverOnStartup(ctx); err != nil {
			return fmt.Errorf("failed to recover gameserver status: %w", err)
		}
		statusMgr.Start(ctx)
	}
	defer statusMgr.Stop()

	// Start scheduler
	if err := scheduler.Start(ctx); err != nil {
		return fmt.Errorf("failed to start scheduler: %w", err)
	}
	defer scheduler.Stop()
	defer readyWatcher.StopAll()
	defer querySvc.StopAll()

	// Start gRPC server for controller and/or local worker agent
	if grpcPort > 0 {
		// Auto-generate TLS certs for controller roles
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
			// Dial-back uses server cert as client cert to connect to workers
			if serverTLS != nil {
				dialBackTLS = &tls.Config{
					Certificates: serverTLS.Certificates,
					RootCAs:      serverTLS.ClientCAs,
				}
			}
		}
		go func() {
			if err := startGRPCServer(localWorker, gameStore, cfg.DataDir, registry, authSvc, database, cfg.BindAddress, grpcPort, serverTLS, dialBackTLS, logger); err != nil {
				logger.Error("grpc server stopped", "error", err)
			}
		}()
	}

	// Start heartbeat reaper for multi-node mode
	if registry != nil {
		registry.StartReaper(ctx, logger)
	}

	netInfo := netinfo.Detect(logger)

	router, err := web.NewRouter(gameStore, gameserverSvc, consoleSvc, fileSvc, scheduleSvc, backupSvc, querySvc, settingsSvc, authSvc, broadcaster, netInfo, registry, database, logPath, cfg.DataDir, cfg.BindAddress, cfg.Port, sftpPort, role, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize router: %w", err)
	}

	// Prune old audit log entries on startup, then daily
	go func() {
		retentionDays := settingsSvc.GetAuditRetentionDays()
		if retentionDays > 0 {
			pruned, err := models.PruneAuditLogs(database, retentionDays)
			if err != nil {
				logger.Error("failed to prune audit logs on startup", "error", err)
			} else if pruned > 0 {
				logger.Info("pruned old audit log entries", "count", pruned, "retention_days", retentionDays)
			}
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			days := settingsSvc.GetAuditRetentionDays()
			if days > 0 {
				if pruned, err := models.PruneAuditLogs(database, days); err != nil {
					logger.Error("failed to prune audit logs", "error", err)
				} else if pruned > 0 {
					logger.Info("pruned old audit log entries", "count", pruned, "retention_days", days)
				}
			}
		}
	}()

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

	if !isLoopback(cfg.BindAddress) && !settingsSvc.GetAuthEnabled() {
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

	srv := &http.Server{
		Addr:              addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return srv.ListenAndServe()
}

// runWorkerAgent starts a worker-only node: gRPC agent wrapping a local Docker worker.
// No database, no web UI, no scheduler.
func runWorkerAgent(cfg config.Config, grpcPort int, controllerAddr string, workerID string, workerToken string, logger *slog.Logger) error {
	if grpcPort == 0 {
		grpcPort = 9090
	}

	dockerClient, err := docker.New(logger)
	if err != nil {
		return fmt.Errorf("failed to connect to docker: %w", err)
	}
	defer dockerClient.Close()

	gameStore, err := games.NewGameStore(filepath.Join(cfg.DataDir, "games"), logger)
	if err != nil {
		return fmt.Errorf("failed to initialize game store: %w", err)
	}

	localWorker := worker.NewLocalWorker(dockerClient, gameStore, cfg.DataDir, logger)

	// Load worker TLS config from env vars
	var workerTLSConfig *tls.Config
	if caPath := os.Getenv("GJ_GRPC_CA"); caPath != "" {
		certPath := os.Getenv("GJ_GRPC_CERT")
		keyPath := os.Getenv("GJ_GRPC_KEY")
		if certPath == "" || keyPath == "" {
			return fmt.Errorf("GJ_GRPC_CA is set but GJ_GRPC_CERT and GJ_GRPC_KEY are also required")
		}
		tlsCfg, err := tlsutil.ClientTLSConfig(caPath, certPath, keyPath)
		if err != nil {
			return fmt.Errorf("failed to load worker TLS config: %w", err)
		}
		workerTLSConfig = tlsCfg
		logger.Info("worker gRPC using mTLS")
	}

	// Worker's own gRPC agent also needs TLS so controller can dial back securely
	var workerServerTLS *tls.Config
	if workerTLSConfig != nil {
		caPool := workerTLSConfig.RootCAs
		workerServerTLS = &tls.Config{
			Certificates: workerTLSConfig.Certificates,
			ClientCAs:    caPool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		}
	}

	// Start gRPC agent in background
	go func() {
		if err := startGRPCServer(localWorker, gameStore, cfg.DataDir, nil, nil, nil, cfg.BindAddress, grpcPort, workerServerTLS, nil, logger); err != nil {
			logger.Error("grpc agent stopped", "error", err)
		}
	}()

	// Start SFTP on worker if port is configured
	workerSFTPPort := 0
	if v := os.Getenv("GJ_SFTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			workerSFTPPort = n
		}
	}
	if workerSFTPPort > 0 && controllerAddr != "" {
		// Connect to controller for SFTP auth validation
		sftpClient, sftpConn, err := worker.DialController(controllerAddr, workerToken, workerTLSConfig)
		if err != nil {
			logger.Warn("failed to connect to controller for sftp auth, sftp disabled", "error", err)
		} else {
			defer sftpConn.Close()
			sftpAuth := gjsftp.NewRemoteAuth(sftpClient)
			fileOp := gjsftp.NewWorkerFileOperator(localWorker)
			fileOpFactory := func(_ string) gjsftp.FileOperator { return fileOp }
			hostKeyPath := filepath.Join(cfg.DataDir, "sftp_host_key")
			sftpServer, err := gjsftp.NewServer(sftpAuth, fileOpFactory, hostKeyPath, logger)
			if err != nil {
				logger.Error("failed to initialize sftp server", "error", err)
			} else {
				defer sftpServer.Close()
				go func() {
					sftpAddr := fmt.Sprintf("%s:%d", cfg.BindAddress, workerSFTPPort)
					if err := sftpServer.ListenAndServe(sftpAddr); err != nil {
						logger.Error("sftp server stopped", "error", err)
					}
				}()
			}
		}
	}

	// If controller address is provided, register with it and start heartbeat loop
	if controllerAddr != "" {
		if workerID == "" {
			workerID, _ = os.Hostname()
			if workerID == "" {
				workerID = fmt.Sprintf("worker-%d", os.Getpid())
			}
		}

		if workerToken == "" {
			logger.Warn("no worker token provided, controller will likely reject registration")
		}

		netInfo := netinfo.Detect(logger)
		ownAddr := fmt.Sprintf("%s:%d", netInfo.LANIP, grpcPort)

		logger.Info("registering with controller",
			"controller", controllerAddr,
			"worker_id", workerID,
			"own_grpc_address", ownAddr,
			"has_token", workerToken != "",
		)

		runRegistrationLoop(controllerAddr, workerID, ownAddr, workerToken, workerSFTPPort, netInfo, workerTLSConfig, logger)
		// runRegistrationLoop blocks forever
	}

	// No controller — just serve gRPC forever
	logger.Info("worker agent running without controller (standalone gRPC)")
	select {}
}

// runRegistrationLoop connects to the controller, registers, and sends heartbeats.
// Reconnects with backoff on failure. Blocks forever.
func runRegistrationLoop(controllerAddr, workerID, ownAddr, workerToken string, sftpPort int, netInfo *netinfo.Info, tlsConfig *tls.Config, logger *slog.Logger) {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		client, conn, err := worker.DialController(controllerAddr, workerToken, tlsConfig)
		if err != nil {
			logger.Error("failed to connect to controller", "error", err, "retry_in", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Register
		ctx := context.Background()
		req := buildHeartbeatRequest(workerID, netInfo)
		regReq := &pb.RegisterRequest{
			WorkerId:          workerID,
			GrpcAddress:       ownAddr,
			CpuCores:          req.CpuCores,
			MemoryTotalMb:     req.MemoryTotalMb,
			MemoryAvailableMb: req.MemoryAvailableMb,
			DiskTotalMb:       req.DiskTotalMb,
			DiskAvailableMb:   req.DiskAvailableMb,
			LanIp:             req.LanIp,
			ExternalIp:        req.ExternalIp,
		}
		if v, err := strconv.Atoi(os.Getenv("GJ_PORT_RANGE_START")); err == nil {
			regReq.PortRangeStart = int32(v)
		}
		if v, err := strconv.Atoi(os.Getenv("GJ_PORT_RANGE_END")); err == nil {
			regReq.PortRangeEnd = int32(v)
		}
		if sftpPort > 0 {
			regReq.SftpPort = int32(sftpPort)
		}
		regResp, err := client.Register(ctx, regReq)
		if err != nil {
			logger.Error("registration failed", "error", err, "retry_in", backoff)
			conn.Close()
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		if !regResp.Accepted {
			logger.Error("registration rejected by controller", "retry_in", backoff)
			conn.Close()
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		logger.Info("registered with controller", "controller", controllerAddr)
		backoff = time.Second // reset on success

		// Heartbeat loop
		ticker := time.NewTicker(10 * time.Second)
		heartbeatFailed := false
		for range ticker.C {
			hbReq := buildHeartbeatRequest(workerID, netInfo)
			resp, err := client.Heartbeat(ctx, hbReq)
			if err != nil {
				logger.Warn("heartbeat failed", "error", err)
				heartbeatFailed = true
				break
			}
			if !resp.Accepted {
				logger.Warn("heartbeat rejected, re-registering")
				heartbeatFailed = true
				break
			}
			logger.Debug("heartbeat sent", "memory_available_mb", hbReq.MemoryAvailableMb)
		}
		ticker.Stop()
		conn.Close()

		if heartbeatFailed {
			logger.Info("reconnecting to controller", "retry_in", backoff)
			time.Sleep(backoff)
		}
	}
}

func buildHeartbeatRequest(workerID string, netInfo *netinfo.Info) *pb.HeartbeatRequest {
	req := &pb.HeartbeatRequest{
		WorkerId:   workerID,
		CpuCores:   int64(runtime.NumCPU()),
		LanIp:      netInfo.LANIP,
		ExternalIp: netInfo.ExternalIP,
	}

	if v, err := mem.VirtualMemory(); err == nil {
		req.MemoryTotalMb = int64(v.Total / 1024 / 1024)
		req.MemoryAvailableMb = int64(v.Available / 1024 / 1024)
	}

	if d, err := disk.Usage("/"); err == nil {
		req.DiskTotalMb = int64(d.Total / 1024 / 1024)
		req.DiskAvailableMb = int64(d.Free / 1024 / 1024)
	}

	return req
}

// startGRPCServer starts a gRPC server with WorkerService and/or ControllerService.
func startGRPCServer(w worker.Worker, gameStore *games.GameStore, dataDir string, registry *worker.Registry, authSvc *service.AuthService, database *sql.DB, bindAddress string, port int, tlsConfig *tls.Config, dialBackTLS *tls.Config, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", bindAddress, port))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}

	var opts []grpc.ServerOption
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(grpcCredentials.NewTLS(tlsConfig)))
		logger.Info("grpc server using mTLS", "port", port)
	}
	// Add auth interceptor when running as controller (registry present)
	if registry != nil {
		opts = append(opts, grpc.UnaryInterceptor(worker.WorkerAuthInterceptor()))
	}
	grpcServer := grpc.NewServer(opts...)

	// Register WorkerService if we have a local worker (worker or controller+worker mode)
	if w != nil {
		agent := worker.NewAgent(w, gameStore, dataDir, logger)
		pb.RegisterWorkerServiceServer(grpcServer, agent)
	}

	// Register ControllerService if we have a registry (controller or controller+worker mode)
	if registry != nil {
		controllerSvc := worker.NewControllerGRPC(registry, authSvc, database, dialBackTLS, logger)
		pb.RegisterControllerServiceServer(grpcServer, controllerSvc)
	}

	logger.Info("grpc server listening", "port", port)
	return grpcServer.Serve(listener)
}

func isLoopback(addr string) bool {
	return addr == "127.0.0.1" || addr == "::1" || addr == "localhost"
}
