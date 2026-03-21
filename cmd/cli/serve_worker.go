package cli

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/config"
	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/netinfo"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	gjsftp "github.com/0xkowalskidev/gamejanitor/internal/sftp"
	"github.com/0xkowalskidev/gamejanitor/internal/tlsutil"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/0xkowalskidev/gamejanitor/internal/worker/pb"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"google.golang.org/grpc"
	grpcCredentials "google.golang.org/grpc/credentials"
)

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
		if v, err := strconv.Atoi(os.Getenv("GJ_MAX_MEMORY")); err == nil && v > 0 {
			regReq.MaxMemoryMb = int64(v)
		}
		if v, err := strconv.ParseFloat(os.Getenv("GJ_MAX_CPU"), 64); err == nil && v > 0 {
			regReq.MaxCpu = v
		}
		if v, err := strconv.Atoi(os.Getenv("GJ_MAX_STORAGE")); err == nil && v > 0 {
			regReq.MaxStorageMb = int64(v)
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
