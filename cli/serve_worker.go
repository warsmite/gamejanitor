package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/docker"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/netinfo"
	"github.com/warsmite/gamejanitor/service"
	gjsftp "github.com/warsmite/gamejanitor/sftp"
	"github.com/warsmite/gamejanitor/tlsutil"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/pb"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	"google.golang.org/grpc"
	grpcCredentials "google.golang.org/grpc/credentials"
)

// runWorkerAgent starts a worker-only node: gRPC agent wrapping a local Docker worker.
// No database, no web UI, no scheduler.
func runWorkerAgent(cfg config.Config, logger *slog.Logger) error {
	grpcPort := cfg.GRPCPort
	if grpcPort == 0 {
		grpcPort = 9090
	}

	gameStore, err := games.NewGameStore(filepath.Join(cfg.DataDir, "games"), logger)
	if err != nil {
		return fmt.Errorf("failed to initialize game store: %w", err)
	}

	var localWorker worker.Worker
	if cfg.ContainerRuntime == "process" {
		localWorker = worker.NewProcessWorker(gameStore, cfg.DataDir, logger)
	} else {
		dockerClient, err := docker.New(logger, cfg.ResolveContainerSocket())
		if err != nil {
			return fmt.Errorf("failed to connect to container runtime: %w", err)
		}
		defer dockerClient.Close()
		localWorker = worker.NewLocalWorker(dockerClient, gameStore, cfg.DataDir, logger)
	}

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
	go func() {
		if err := startGRPCServer(localWorker, gameStore, cfg.DataDir, nil, nil, nil, cfg.Bind, grpcPort, workerServerTLS, nil, nil, nil, logger); err != nil {
			logger.Error("grpc agent stopped", "error", err)
		}
	}()

	// Start SFTP on worker if port is configured
	if cfg.SFTPPort > 0 && cfg.ControllerAddress != "" {
		sftpClient, sftpConn, err := worker.DialController(cfg.ControllerAddress, cfg.WorkerToken, workerTLSConfig)
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
			workerID, _ = os.Hostname()
			if workerID == "" {
				workerID = fmt.Sprintf("worker-%d", os.Getpid())
			}
		}

		if cfg.WorkerToken == "" {
			logger.Warn("no worker token provided, controller will likely reject registration")
		}

		logger.Info("connecting to controller",
			"controller", cfg.ControllerAddress,
			"worker_id", workerID,
			"has_token", cfg.WorkerToken != "",
		)

		runRegistrationLoop(cfg, workerID, grpcPort, workerTLSConfig, logger)
		// runRegistrationLoop blocks forever
	}

	// No controller — just serve gRPC forever
	logger.Info("worker agent running without controller (standalone gRPC)")
	select {}
}

// loadWorkerTLS loads TLS config from explicit config or auto-discovery.
func loadWorkerTLS(cfg config.Config, logger *slog.Logger) *tls.Config {
	if cfg.TLS != nil && cfg.TLS.CA != "" {
		if cfg.TLS.Cert == "" || cfg.TLS.Key == "" {
			logger.Error("tls.ca is set but tls.cert and tls.key are also required")
			return nil
		}
		tlsCfg, err := tlsutil.ClientTLSConfig(cfg.TLS.CA, cfg.TLS.Cert, cfg.TLS.Key)
		if err != nil {
			logger.Error("failed to load worker TLS config", "error", err)
			return nil
		}
		logger.Info("worker gRPC using mTLS (from config)")
		return tlsCfg
	}

	// Auto-discovery: check {data_dir}/certs/
	caPath := filepath.Join(cfg.DataDir, "certs", "ca.pem")
	certPath := filepath.Join(cfg.DataDir, "certs", "cert.pem")
	keyPath := filepath.Join(cfg.DataDir, "certs", "key.pem")
	if _, err := os.Stat(caPath); err == nil {
		tlsCfg, err := tlsutil.ClientTLSConfig(caPath, certPath, keyPath)
		if err != nil {
			logger.Error("failed to load auto-discovered TLS config", "error", err)
			return nil
		}
		logger.Info("worker gRPC using mTLS (auto-discovered from data_dir/certs)")
		return tlsCfg
	}

	return nil
}

// enrollWithController connects to the controller without a client cert to call Register
// and obtain TLS certificates. Saves the issued certs to {dataDir}/certs/ for future use.
// Retries with backoff until enrollment succeeds.
func enrollWithController(cfg config.Config, grpcPort int, logger *slog.Logger) *tls.Config {
	workerID := cfg.WorkerID
	if workerID == "" {
		workerID, _ = os.Hostname()
		if workerID == "" {
			workerID = fmt.Sprintf("worker-%d", os.Getpid())
		}
	}

	logger.Info("enrolling with controller for TLS certificates",
		"controller", cfg.ControllerAddress,
		"worker_id", workerID,
	)

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		// Re-detect IPs each attempt so we recover if network wasn't ready at startup
		netInfo := netinfo.Detect(logger)
		ownAddr := fmt.Sprintf("%s:%d", netInfo.LANIP, grpcPort)

		client, conn, err := worker.DialControllerEnrollment(cfg.ControllerAddress, cfg.WorkerToken)
		if err != nil {
			logger.Error("failed to connect to controller for enrollment", "error", err, "retry_in", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		regReq := &pb.RegisterRequest{
			WorkerId:    workerID,
			GrpcAddress: ownAddr,
			LanIp:       netInfo.LANIP,
			ExternalIp:  netInfo.ExternalIP,
		}

		// Include resource info
		hb := buildHeartbeatRequest(workerID, netInfo)
		regReq.CpuCores = hb.CpuCores
		regReq.MemoryTotalMb = hb.MemoryTotalMb
		regReq.MemoryAvailableMb = hb.MemoryAvailableMb
		regReq.DiskTotalMb = hb.DiskTotalMb
		regReq.DiskAvailableMb = hb.DiskAvailableMb

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

		resp, err := client.Register(context.Background(), regReq)
		conn.Close()

		if err != nil {
			logger.Error("enrollment registration failed", "error", err, "retry_in", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		if !resp.Accepted {
			logger.Error("enrollment rejected by controller", "retry_in", backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		if len(resp.CaCertPem) == 0 {
			logger.Warn("controller accepted registration but did not issue certs, mTLS unavailable")
			return nil
		}

		// Save issued certs
		certsDir := filepath.Join(cfg.DataDir, "certs")
		if err := os.MkdirAll(certsDir, 0700); err != nil {
			logger.Error("failed to create certs directory", "error", err)
			return nil
		}
		if err := os.WriteFile(filepath.Join(certsDir, "ca.pem"), resp.CaCertPem, 0644); err != nil {
			logger.Error("failed to save CA cert", "error", err)
			return nil
		}
		if err := os.WriteFile(filepath.Join(certsDir, "cert.pem"), resp.ClientCertPem, 0644); err != nil {
			logger.Error("failed to save client cert", "error", err)
			return nil
		}
		if err := os.WriteFile(filepath.Join(certsDir, "key.pem"), resp.ClientKeyPem, 0600); err != nil {
			logger.Error("failed to save client key", "error", err)
			return nil
		}

		logger.Info("TLS certificates saved, loading mTLS config")
		tlsCfg, err := tlsutil.ClientTLSConfig(
			filepath.Join(certsDir, "ca.pem"),
			filepath.Join(certsDir, "cert.pem"),
			filepath.Join(certsDir, "key.pem"),
		)
		if err != nil {
			logger.Error("failed to load enrolled TLS config", "error", err)
			return nil
		}

		logger.Info("enrollment complete, worker has mTLS certificates")
		return tlsCfg
	}
}

// runRegistrationLoop connects to the controller, registers, and sends heartbeats.
// Reconnects with backoff on failure. Blocks forever.
// Re-detects network info on each registration attempt so that workers recover
// from boot-time detection failures (e.g. network not ready after power cut).
func runRegistrationLoop(cfg config.Config, workerID string, grpcPort int, tlsConfig *tls.Config, logger *slog.Logger) {
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		// Re-detect IPs each attempt so we recover if network wasn't ready at startup
		netInfo := netinfo.Detect(logger)
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

		client, conn, err := worker.DialController(cfg.ControllerAddress, cfg.WorkerToken, tlsConfig)
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

		logger.Info("registered with controller", "controller", cfg.ControllerAddress)
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

func startGRPCServer(w worker.Worker, gameStore *games.GameStore, dataDir string, registry *worker.Registry, authSvc *service.AuthService, database *sql.DB, bindAddress string, port int, tlsConfig *tls.Config, dialBackTLS *tls.Config, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, logger *slog.Logger) error {
	addr := fmt.Sprintf("%s:%d", bindAddress, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return listenError("gRPC", addr, port, err)
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
		controllerSvc := worker.NewControllerGRPC(registry, authSvc, database, dialBackTLS, caCert, caKey, logger)
		pb.RegisterControllerServiceServer(grpcServer, controllerSvc)
	}

	logger.Info("grpc server listening", "port", port)
	return grpcServer.Serve(listener)
}
