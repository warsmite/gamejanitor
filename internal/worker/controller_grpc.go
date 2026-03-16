package worker

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker/pb"
	"google.golang.org/grpc"
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
	registry  *Registry
	tokenAuth TokenValidator
	log       *slog.Logger
}

func NewControllerGRPC(registry *Registry, tokenAuth TokenValidator, log *slog.Logger) *ControllerGRPC {
	return &ControllerGRPC{registry: registry, tokenAuth: tokenAuth, log: log}
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
	conn, err := grpc.NewClient(req.GrpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
		TokenID:           token.ID,
	}

	c.registry.Register(remote, info)
	c.log.Info("worker registered successfully", "worker_id", req.WorkerId, "grpc_address", req.GrpcAddress, "token_id", token.ID)
	return &pb.RegisterResponse{Accepted: true}, nil
}

func (c *ControllerGRPC) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	info := WorkerInfo{
		ID:                req.WorkerId,
		CPUCores:          req.CpuCores,
		MemoryTotalMB:     req.MemoryTotalMb,
		MemoryAvailableMB: req.MemoryAvailableMb,
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

	c.log.Debug("heartbeat received", "worker_id", req.WorkerId, "memory_available_mb", req.MemoryAvailableMb)
	return &pb.HeartbeatResponse{Accepted: true}, nil
}

// DialController connects to a controller's gRPC address and returns the ControllerService client.
// If token is non-empty, it is sent as Bearer auth metadata on every RPC.
func DialController(address string, token string) (pb.ControllerServiceClient, *grpc.ClientConn, error) {
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	if token != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(workerCredentials{token: token}))
	}
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("dialing controller at %s: %w", address, err)
	}
	return pb.NewControllerServiceClient(conn), conn, nil
}
