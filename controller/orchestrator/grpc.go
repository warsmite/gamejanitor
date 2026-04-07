package orchestrator

import (
	"github.com/warsmite/gamejanitor/worker/agent"
	"github.com/warsmite/gamejanitor/worker/remote"
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/tlsutil"
	"github.com/warsmite/gamejanitor/worker/pb"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// GRPCStore defines the persistence methods the gRPC controller service needs.
type GRPCStore interface {
	UpsertWorkerNode(node *model.WorkerNode) error
	GetWorkerNode(id string) (*model.WorkerNode, error)
	SetWorkerNodeSFTPPort(id string, sftpPort int) error
	SetWorkerNodeLimits(id string, maxMemoryMB *int, maxCPU *float64, maxStorageMB *int) error
	GetGameserverBySFTPUsername(username string) (*model.Gameserver, error)
}

// TokenValidator provides token validation for gRPC auth without importing the service package.
type TokenValidator interface {
	ValidateToken(rawToken string) *model.Token
	IsWorkerTokenValid(tokenID string) bool
}

// ControllerGRPC implements ControllerServiceServer.
// Runs on the controller; accepts worker registrations and heartbeats.
type ControllerGRPC struct {
	pb.UnimplementedControllerServiceServer
	registry    *Registry
	tokenAuth   TokenValidator
	store       GRPCStore
	dialBackTLS *tls.Config
	caCert      *x509.Certificate
	caKey       *ecdsa.PrivateKey
	log         *slog.Logger
}

func NewControllerGRPC(registry *Registry, tokenAuth TokenValidator, store GRPCStore, dialBackTLS *tls.Config, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, log *slog.Logger) *ControllerGRPC {
	return &ControllerGRPC{registry: registry, tokenAuth: tokenAuth, store: store, dialBackTLS: dialBackTLS, caCert: caCert, caKey: caKey, log: log}
}

func (c *ControllerGRPC) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	c.log.Info("worker registration request",
		"worker", req.WorkerId,
		"grpc_address", req.GrpcAddress,
		"cpu_cores", req.CpuCores,
		"memory_total_mb", req.MemoryTotalMb,
		"memory_available_mb", req.MemoryAvailableMb,
		"lan_ip", req.LanIp,
		"external_ip", req.ExternalIp,
	)

	// Validate worker token
	rawToken := agent.TokenFromContext(ctx)
	token := c.tokenAuth.ValidateToken(rawToken)
	if token == nil || token.Role != "worker" {
		c.log.Warn("worker registration rejected: invalid or non-worker token", "worker", req.WorkerId)
		return &pb.RegisterResponse{Accepted: false}, nil
	}

	// Generate worker TLS certificate for mTLS enrollment
	var caPEM, certPEM, keyPEM []byte
	if c.caCert != nil && c.caKey != nil {
		workerIPs := parseWorkerIPs(req.LanIp, req.ExternalIp)
		var err error
		caPEM, certPEM, keyPEM, err = tlsutil.GenerateWorkerCertPEM(req.WorkerId, c.caCert, c.caKey, workerIPs)
		if err != nil {
			c.log.Error("failed to generate worker cert", "worker", req.WorkerId, "error", err)
			return &pb.RegisterResponse{Accepted: false}, nil
		}
		c.log.Info("issued TLS certificate for worker", "worker", req.WorkerId)
	}

	// Persist worker node (dial-back happens on first heartbeat after worker has certs)
	if err := c.store.UpsertWorkerNode(&model.WorkerNode{
		ID: req.WorkerId, Name: req.Name, GRPCAddress: req.GrpcAddress, LanIP: req.LanIp, ExternalIP: req.ExternalIp,
	}); err != nil {
		c.log.Error("failed to persist worker node on register", "worker", req.WorkerId, "error", err)
	}

	// Persist worker-reported SFTP port if provided
	if req.SftpPort > 0 {
		if err := c.store.SetWorkerNodeSFTPPort(req.WorkerId, int(req.SftpPort)); err != nil {
			c.log.Error("failed to set worker sftp port on register", "worker", req.WorkerId, "error", err)
		}
	}

	// Persist worker-reported resource limits (ENV-configured on worker takes precedence over API)
	if req.MaxMemoryMb > 0 || req.MaxCpu > 0 || req.MaxStorageMb > 0 {
		var maxMem *int
		var maxCPU *float64
		var maxStorage *int
		if req.MaxMemoryMb > 0 {
			v := int(req.MaxMemoryMb)
			maxMem = &v
		}
		if req.MaxCpu > 0 {
			v := req.MaxCpu
			maxCPU = &v
		}
		if req.MaxStorageMb > 0 {
			v := int(req.MaxStorageMb)
			maxStorage = &v
		}
		// Only update fields the worker explicitly set — read existing first to preserve API-configured values
		if existing, err := c.store.GetWorkerNode(req.WorkerId); err == nil && existing != nil {
			if maxMem == nil {
				maxMem = existing.MaxMemoryMB
			}
			if maxCPU == nil {
				maxCPU = existing.MaxCPU
			}
			if maxStorage == nil {
				maxStorage = existing.MaxStorageMB
			}
		}
		if err := c.store.SetWorkerNodeLimits(req.WorkerId, maxMem, maxCPU, maxStorage); err != nil {
			c.log.Error("failed to set worker resource limits on register", "worker", req.WorkerId, "error", err)
		}
	}

	c.log.Info("worker registered successfully", "worker", req.WorkerId, "grpc_address", req.GrpcAddress, "token", token.ID)
	return &pb.RegisterResponse{
		Accepted:      true,
		CaCertPem:     caPEM,
		ClientCertPem: certPEM,
		ClientKeyPem:  keyPEM,
	}, nil
}

func (c *ControllerGRPC) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	info := WorkerInfo{
		ID:                req.WorkerId,
		CPUCores:          req.CpuCores,
		MemoryTotalMB:     req.MemoryTotalMb,
		MemoryAvailableMB: req.MemoryAvailableMb,
		DiskTotalMB:       req.DiskTotalMb,
		DiskAvailableMB:   req.DiskAvailableMb,
		LanIP:             req.LanIp,
		ExternalIP:        req.ExternalIp,
	}

	if err := c.registry.UpdateHeartbeat(req.WorkerId, info); err != nil {
		// Worker not in registry — first heartbeat after enrollment. Establish dial-back.
		rawToken := agent.TokenFromContext(ctx)
		token := c.tokenAuth.ValidateToken(rawToken)
		if token == nil || token.Role != "worker" {
			c.log.Warn("heartbeat rejected: invalid or non-worker token", "worker", req.WorkerId)
			return &pb.HeartbeatResponse{Accepted: false}, nil
		}

		node, err := c.store.GetWorkerNode(req.WorkerId)
		if err != nil || node == nil {
			c.log.Warn("heartbeat from unknown worker", "worker", req.WorkerId)
			return &pb.HeartbeatResponse{Accepted: false}, nil
		}

		if node.GRPCAddress == "" {
			c.log.Error("worker has no grpc_address, cannot dial back", "worker", req.WorkerId)
			return &pb.HeartbeatResponse{Accepted: false}, nil
		}

		// Dial back to worker with mTLS
		var dialOpt grpc.DialOption
		if c.dialBackTLS != nil {
			dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(c.dialBackTLS))
		} else {
			dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
		}
		conn, err := grpc.NewClient(node.GRPCAddress, dialOpt)
		if err != nil {
			c.log.Error("failed to dial worker on first heartbeat", "worker", req.WorkerId, "address", node.GRPCAddress, "error", err)
			return &pb.HeartbeatResponse{Accepted: false}, nil
		}

		// Verify connectivity
		hbClient := pb.NewWorkerServiceClient(conn)
		_, err = hbClient.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: req.WorkerId})
		if err != nil {
			conn.Close()
			c.log.Error("worker dial-back health check failed", "worker", req.WorkerId, "error", err)
			return &pb.HeartbeatResponse{Accepted: false}, nil
		}

		info.TokenID = token.ID
		remote := remote.New(conn, req.WorkerId)
		c.registry.Register(req.WorkerId, remote, info)
		c.log.Info("worker activated via first heartbeat", "worker", req.WorkerId, "grpc_address", node.GRPCAddress)
	}

	if err := c.store.UpsertWorkerNode(&model.WorkerNode{
		ID: req.WorkerId, LanIP: req.LanIp, ExternalIP: req.ExternalIp,
	}); err != nil {
		c.log.Error("failed to persist worker node on heartbeat", "worker", req.WorkerId, "error", err)
	}

	c.log.Debug("heartbeat received", "worker", req.WorkerId, "memory_available_mb", req.MemoryAvailableMb)
	return &pb.HeartbeatResponse{Accepted: true}, nil
}

func (c *ControllerGRPC) ValidateSFTPLogin(ctx context.Context, req *pb.SFTPLoginRequest) (*pb.SFTPLoginResponse, error) {
	gs, err := c.store.GetGameserverBySFTPUsername(req.Username)
	if err != nil {
		c.log.Error("sftp login lookup failed", "username", req.Username, "error", err)
		return &pb.SFTPLoginResponse{Valid: false}, nil
	}
	if gs == nil || bcrypt.CompareHashAndPassword([]byte(gs.HashedSFTPPassword), []byte(req.Password)) != nil {
		return &pb.SFTPLoginResponse{Valid: false}, nil
	}
	return &pb.SFTPLoginResponse{
		Valid:        true,
		GameserverId: gs.ID,
		VolumeName:   gs.VolumeName,
	}, nil
}

// DialController connects to a controller's gRPC address and returns the ControllerService client.
// If token is non-empty, it is sent as Bearer auth metadata on every RPC.
// If tlsConfig is non-nil, the connection uses mTLS; otherwise insecure.
func DialController(address string, token string, tlsConfig *tls.Config) (pb.ControllerServiceClient, *grpc.ClientConn, error) {
	var transportCreds grpc.DialOption
	if tlsConfig != nil {
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	} else {
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	opts := []grpc.DialOption{transportCreds}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(agent.WorkerCredentials{Token: token, RequireTLS: tlsConfig != nil}))
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing controller at %s: %w", address, err)
	}
	return pb.NewControllerServiceClient(conn), conn, nil
}

// DialControllerEnrollment connects to the controller with TLS (encrypted) but without a client
// certificate. Used for the initial Register call before the worker has been issued certs.
// Token auth protects the RPC; InsecureSkipVerify is acceptable because the controller uses a
// self-signed CA and the worker validates the full cert chain on all subsequent connections.
func DialControllerEnrollment(address string, token string) (pb.ControllerServiceClient, *grpc.ClientConn, error) {
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(agent.WorkerCredentials{Token: token, RequireTLS: true}))
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing controller for enrollment at %s: %w", address, err)
	}
	return pb.NewControllerServiceClient(conn), conn, nil
}

func parseWorkerIPs(lanIP, externalIP string) []net.IP {
	var ips []net.IP
	if ip := net.ParseIP(lanIP); ip != nil {
		ips = append(ips, ip)
	}
	if ip := net.ParseIP(externalIP); ip != nil {
		ips = append(ips, ip)
	}
	return ips
}
