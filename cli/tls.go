package cli

import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/util/tlsutil"
	pb "github.com/warsmite/gamejanitor/worker/proto"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
)

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
		"worker", workerID,
	)

	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		// Re-detect IPs each attempt so we recover if network wasn't ready at startup
		netInfo := detectNetInfo(logger)
		ownAddr := fmt.Sprintf("%s:%d", netInfo.LANIP, grpcPort)

		client, conn, err := cluster.DialControllerEnrollment(cfg.ControllerAddress, cfg.WorkerToken)
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

// buildHeartbeatRequest constructs a heartbeat request with system resource info.
func buildHeartbeatRequest(workerID string, netInfo *NetInfo) *pb.HeartbeatRequest {
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
