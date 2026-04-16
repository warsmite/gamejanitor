package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/games"
	gjsftp "github.com/warsmite/gamejanitor/sftp"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/agent"
	pb "github.com/warsmite/gamejanitor/worker/proto"
	"github.com/warsmite/gamejanitor/worker/local"
	"google.golang.org/grpc"
	grpcCredentials "google.golang.org/grpc/credentials"
)

// runWorkerAgent starts a worker-only node: gRPC agent wrapping a local sandbox worker.
// No database, no web UI, no scheduler.
func runWorkerAgent(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	grpcPort := cfg.GRPCPort
	if grpcPort == 0 {
		grpcPort = 9090
	}

	gameStore, err := games.NewGameStore(filepath.Join(cfg.DataDir, "games"), logger)
	if err != nil {
		return fmt.Errorf("failed to initialize game store: %w", err)
	}

	localWorker := local.New(gameStore, cfg.DataDir, logger)

	// Load worker TLS config from config file or auto-discovery
	workerTLSConfig := loadWorkerTLS(cfg, logger)

	// If no certs and we have a controller, enroll to get certs
	if workerTLSConfig == nil && cfg.ControllerAddress != "" {
		workerTLSConfig = enrollWithController(cfg, grpcPort, logger)
	}

	// Worker's own gRPC agent also needs TLS so controller can dial back securely
	var workerServerTLS *tls.Config
	if workerTLSConfig != nil {
		workerServerTLS = &tls.Config{
			Certificates: workerTLSConfig.Certificates,
			ClientCAs:    workerTLSConfig.RootCAs,
			ClientAuth:   tls.RequireAndVerifyClientCert,
		}
	}

	// Start gRPC agent in background
	grpcServer := newGRPCServer(localWorker, gameStore, cfg.DataDir, nil, nil, nil, cfg.Bind, grpcPort, workerServerTLS, nil, nil, nil, logger)
	go func() {
		if err := grpcServer.serve(); err != nil {
			logger.Error("grpc agent stopped", "error", err)
		}
	}()

	// Start SFTP on worker if port is configured
	if cfg.SFTPPort > 0 && cfg.ControllerAddress != "" {
		sftpClient, sftpConn, err := cluster.DialController(cfg.ControllerAddress, cfg.WorkerToken, workerTLSConfig)
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
					sftpAddr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.SFTPPort)
					if err := sftpServer.ListenAndServe(sftpAddr); err != nil {
						logger.Error("sftp server stopped", "error", listenError("sftp", sftpAddr, cfg.SFTPPort, err))
					}
				}()
			}
		}
	}

	// If controller address is provided, start heartbeat loop
	if cfg.ControllerAddress != "" {
		workerID := cfg.WorkerID
		if workerID == "" {
			workerID = loadOrGenerateWorkerID(cfg.DataDir, logger)
		}

		if cfg.WorkerToken == "" {
			logger.Warn("no worker token provided, controller will likely reject registration")
		}

		logger.Info("connecting to controller",
			"controller", cfg.ControllerAddress,
			"worker", workerID,
			"has_token", cfg.WorkerToken != "",
		)

		runRegistrationLoop(ctx, cfg, workerID, grpcPort, workerTLSConfig, logger)
	}

	// No controller — just serve gRPC until shutdown signal
	logger.Info("worker agent running without controller (standalone gRPC)")
	<-ctx.Done()
	logger.Info("shutting down worker agent")
	grpcServer.gracefulStop()
	return nil
}

// runRegistrationLoop connects to the controller, registers, and sends heartbeats.
// Reconnects with backoff on failure. Blocks until context is cancelled.
// Re-detects network info on each registration attempt so that workers recover
// from boot-time detection failures (e.g. network not ready after power cut).
func runRegistrationLoop(ctx context.Context, cfg config.Config, workerID string, grpcPort int, tlsConfig *tls.Config, logger *slog.Logger) {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for ctx.Err() == nil {
		// Re-detect IPs each attempt so we recover if network wasn't ready at startup
		netInfo := detectNetInfo(logger)
		ownAddr := fmt.Sprintf("%s:%d", netInfo.LANIP, grpcPort)
		if cfg.AdvertiseAddress != "" {
			ownAddr = cfg.AdvertiseAddress
		}

		if isLoopback(cfg.Bind) && cfg.AdvertiseAddress == "" {
			logger.Warn("worker gRPC is bound to loopback but reporting LAN IP to controller — controller will not be able to dial back, bind to 0.0.0.0 or your LAN IP for multi-node",
				"bind", cfg.Bind,
				"reported_address", ownAddr,
			)
		}

		client, conn, err := cluster.DialController(cfg.ControllerAddress, cfg.WorkerToken, tlsConfig)
		if err != nil {
			logger.Error("failed to connect to controller", "error", err, "retry_in", backoff)
			sleepCtx(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		req := buildHeartbeatRequest(workerID, netInfo)
		hostname, _ := os.Hostname()
		regReq := &pb.RegisterRequest{
			WorkerId:          workerID,
			Name:              hostname,
			GrpcAddress:       ownAddr,
			CpuCores:          req.CpuCores,
			MemoryTotalMb:     req.MemoryTotalMb,
			MemoryAvailableMb: req.MemoryAvailableMb,
			DiskTotalMb:       req.DiskTotalMb,
			DiskAvailableMb:   req.DiskAvailableMb,
			LanIp:             req.LanIp,
			ExternalIp:        req.ExternalIp,
		}

		// Report worker limits from config
		if wl := cfg.WorkerLimits; wl != nil {
			if wl.MaxMemoryMB > 0 {
				regReq.MaxMemoryMb = int64(wl.MaxMemoryMB)
			}
			if wl.MaxCPU > 0 {
				regReq.MaxCpu = wl.MaxCPU
			}
			if wl.MaxStorageMB > 0 {
				regReq.MaxStorageMb = int64(wl.MaxStorageMB)
			}
		}

		if cfg.SFTPPort > 0 {
			regReq.SftpPort = int32(cfg.SFTPPort)
		}

		regResp, err := client.Register(ctx, regReq)
		if err != nil {
			logger.Error("registration failed", "error", err, "retry_in", backoff)
			conn.Close()
			sleepCtx(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		if !regResp.Accepted {
			logger.Error("registration rejected by controller", "retry_in", backoff)
			conn.Close()
			sleepCtx(ctx, backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		logger.Info("registered with controller", "controller", cfg.ControllerAddress)
		backoff = time.Second // reset on success

		// Heartbeat loop — send first heartbeat immediately so the controller
		// can establish dial-back without waiting for the ticker interval.
		ticker := time.NewTicker(10 * time.Second)
		heartbeatFailed := false
		firstHeartbeat := true
		for ctx.Err() == nil {
			if !firstHeartbeat {
				select {
				case <-ctx.Done():
				case <-ticker.C:
				}
				if ctx.Err() != nil {
					break
				}
			}
			firstHeartbeat = false
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

		if heartbeatFailed && ctx.Err() == nil {
			logger.Info("reconnecting to controller", "retry_in", backoff)
			sleepCtx(ctx, backoff)
		}
	}
}

// sleepCtx sleeps for the given duration or until the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// grpcHandle wraps a gRPC server with its listener for lifecycle management.
type grpcHandle struct {
	server   *grpc.Server
	listener net.Listener
	logger   *slog.Logger
}

func newGRPCServer(w worker.Worker, gameStore *games.GameStore, dataDir string, registry *cluster.Registry, authSvc *auth.AuthService, grpcStore cluster.GRPCStore, bindAddress string, port int, tlsConfig *tls.Config, dialBackTLS *tls.Config, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, logger *slog.Logger) *grpcHandle {
	addr := fmt.Sprintf("%s:%d", bindAddress, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		logger.Error("failed to start grpc listener", "error", listenError("gRPC", addr, port, err))
		return &grpcHandle{logger: logger}
	}

	var opts []grpc.ServerOption
	if tlsConfig != nil {
		opts = append(opts, grpc.Creds(grpcCredentials.NewTLS(tlsConfig)))
		logger.Info("grpc server using mTLS", "port", port)
	}
	// Add auth interceptor when running as controller (registry present)
	if registry != nil {
		opts = append(opts, grpc.UnaryInterceptor(agent.WorkerAuthInterceptor()))
	}
	server := grpc.NewServer(opts...)

	// Register WorkerService if we have a local worker (worker or controller+worker mode)
	if w != nil {
		agentSvc := agent.New(w, gameStore, dataDir, logger)
		pb.RegisterWorkerServiceServer(server, agentSvc)
	}

	// Register ControllerService if we have a registry (controller or controller+worker mode)
	if registry != nil {
		controllerSvc := cluster.NewControllerGRPC(registry, authSvc, grpcStore, dialBackTLS, caCert, caKey, logger)
		pb.RegisterControllerServiceServer(server, controllerSvc)
	}

	return &grpcHandle{server: server, listener: listener, logger: logger}
}

func (h *grpcHandle) serve() error {
	if h.server == nil {
		return fmt.Errorf("grpc server not initialized")
	}
	h.logger.Info("grpc server listening", "addr", h.listener.Addr())
	return h.server.Serve(h.listener)
}

func (h *grpcHandle) gracefulStop() {
	if h.server == nil {
		return
	}
	h.logger.Info("stopping grpc server")
	h.server.GracefulStop()
}

// loadOrGenerateWorkerID reads a persisted worker ID from {dataDir}/worker-id,
// or generates a new UUID and saves it. This ensures the worker has a stable
// identity across restarts, independent of hostname changes.
func loadOrGenerateWorkerID(dataDir string, logger *slog.Logger) string {
	idPath := filepath.Join(dataDir, "worker-id")

	if data, err := os.ReadFile(idPath); err == nil {
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}

	id := fmt.Sprintf("w-%s", generateShortID())
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		logger.Error("failed to create data dir for worker ID", "error", err)
		return id
	}
	if err := os.WriteFile(idPath, []byte(id), 0644); err != nil {
		logger.Error("failed to persist worker ID", "path", idPath, "error", err)
	} else {
		logger.Info("generated worker ID", "id", id, "path", idPath)
	}
	return id
}

// generateShortID produces a short random hex string for worker IDs.

// Not a full UUID — readable enough for CLI/UI use.
func generateShortID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
