package worker

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"golang.org/x/crypto/bcrypt"
	"github.com/0xkowalskidev/gamejanitor/internal/worker/pb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// TokenValidator provides token validation for gRPC auth without importing the service package.
type TokenValidator interface {
	ValidateToken(rawToken string) *models.Token
	IsWorkerTokenValid(tokenID string) bool
}

// ControllerGRPC implements ControllerServiceServer.
// Runs on the controller; accepts worker registrations and heartbeats.
type ControllerGRPC struct {
	pb.UnimplementedControllerServiceServer
	registry    *Registry
	tokenAuth   TokenValidator
	db          *sql.DB
	dialBackTLS *tls.Config
	log         *slog.Logger
}

func NewControllerGRPC(registry *Registry, tokenAuth TokenValidator, db *sql.DB, dialBackTLS *tls.Config, log *slog.Logger) *ControllerGRPC {
	return &ControllerGRPC{registry: registry, tokenAuth: tokenAuth, db: db, dialBackTLS: dialBackTLS, log: log}
}

func (c *ControllerGRPC) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	c.log.Info("worker registration request",
		"worker_id", req.WorkerId,
		"grpc_address", req.GrpcAddress,
		"cpu_cores", req.CpuCores,
		"memory_total_mb", req.MemoryTotalMb,
		"memory_available_mb", req.MemoryAvailableMb,
		"lan_ip", req.LanIp,
		"external_ip", req.ExternalIp,
	)

	// Validate worker token
	rawToken := TokenFromContext(ctx)
	token := c.tokenAuth.ValidateToken(rawToken)
	if token == nil || token.Scope != "worker" {
		c.log.Warn("worker registration rejected: invalid or non-worker token", "worker_id", req.WorkerId)
		return &pb.RegisterResponse{Accepted: false}, nil
	}

	// Dial back to the worker's gRPC address to create a RemoteWorker
	var dialOpt grpc.DialOption
	if c.dialBackTLS != nil {
		dialOpt = grpc.WithTransportCredentials(credentials.NewTLS(c.dialBackTLS))
	} else {
		dialOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	conn, err := grpc.NewClient(req.GrpcAddress, dialOpt)
	if err != nil {
		c.log.Error("failed to dial worker", "worker_id", req.WorkerId, "address", req.GrpcAddress, "error", err)
		return &pb.RegisterResponse{Accepted: false}, nil
	}

	// Verify connectivity with a quick health check
	hbClient := pb.NewWorkerServiceClient(conn)
	_, err = hbClient.Heartbeat(ctx, &pb.HeartbeatRequest{WorkerId: req.WorkerId})
	if err != nil {
		conn.Close()
		c.log.Error("worker health check failed", "worker_id", req.WorkerId, "error", err)
		return &pb.RegisterResponse{Accepted: false}, nil
	}

	remote := NewRemoteWorker(conn, req.WorkerId)
	info := WorkerInfo{
		ID:                req.WorkerId,
		LanIP:             req.LanIp,
		ExternalIP:        req.ExternalIp,
		CPUCores:          req.CpuCores,
		MemoryTotalMB:     req.MemoryTotalMb,
		MemoryAvailableMB: req.MemoryAvailableMb,
		DiskTotalMB:       req.DiskTotalMb,
		DiskAvailableMB:   req.DiskAvailableMb,
		TokenID:           token.ID,
	}

	c.registry.Register(remote, info)

	if err := models.UpsertWorkerNode(c.db, &models.WorkerNode{
		ID: req.WorkerId, LanIP: req.LanIp, ExternalIP: req.ExternalIp,
	}); err != nil {
		c.log.Error("failed to persist worker node on register", "worker_id", req.WorkerId, "error", err)
	}

	// Persist worker-reported SFTP port if provided
	if req.SftpPort > 0 {
		if err := models.SetWorkerNodeSFTPPort(c.db, req.WorkerId, int(req.SftpPort)); err != nil {
			c.log.Error("failed to set worker sftp port on register", "worker_id", req.WorkerId, "error", err)
		}
	}

	// Persist worker-reported port range if provided
	if req.PortRangeStart > 0 && req.PortRangeEnd > 0 {
		start, end := int(req.PortRangeStart), int(req.PortRangeEnd)
		if err := models.SetWorkerNodePortRange(c.db, req.WorkerId, &start, &end); err != nil {
			c.log.Error("failed to set worker port range on register", "worker_id", req.WorkerId, "error", err)
		}
	}

	c.log.Info("worker registered successfully", "worker_id", req.WorkerId, "grpc_address", req.GrpcAddress, "token_id", token.ID)
	return &pb.RegisterResponse{Accepted: true}, nil
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
		c.log.Debug("heartbeat from unregistered worker", "worker_id", req.WorkerId)
		return &pb.HeartbeatResponse{Accepted: false}, nil
	}

	// Check that the worker's token still exists (lightweight, no bcrypt)
	storedInfo, ok := c.registry.GetInfo(req.WorkerId)
	if !ok || !c.tokenAuth.IsWorkerTokenValid(storedInfo.TokenID) {
		c.log.Warn("worker token revoked, rejecting heartbeat", "worker_id", req.WorkerId, "token_id", storedInfo.TokenID)
		return &pb.HeartbeatResponse{Accepted: false}, nil
	}

	if err := models.UpsertWorkerNode(c.db, &models.WorkerNode{
		ID: req.WorkerId, LanIP: req.LanIp, ExternalIP: req.ExternalIp,
	}); err != nil {
		c.log.Error("failed to persist worker node on heartbeat", "worker_id", req.WorkerId, "error", err)
	}

	c.log.Debug("heartbeat received", "worker_id", req.WorkerId, "memory_available_mb", req.MemoryAvailableMb)
	return &pb.HeartbeatResponse{Accepted: true}, nil
}

func (c *ControllerGRPC) ValidateSFTPLogin(ctx context.Context, req *pb.SFTPLoginRequest) (*pb.SFTPLoginResponse, error) {
	gs, err := models.GetGameserverBySFTPUsername(c.db, req.Username)
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
// If tlsConfig is non-nil, the connection uses TLS; otherwise insecure.
func DialController(address string, token string, tlsConfig *tls.Config) (pb.ControllerServiceClient, *grpc.ClientConn, error) {
	var transportCreds grpc.DialOption
	if tlsConfig != nil {
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig))
	} else {
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	opts := []grpc.DialOption{transportCreds}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(workerCredentials{token: token, requireTLS: tlsConfig != nil}))
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing controller at %s: %w", address, err)
	}
	return pb.NewControllerServiceClient(conn), conn, nil
}
