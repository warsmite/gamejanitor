package worker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/worker/pb"
)

// Agent wraps a LocalWorker and exposes it as a gRPC service.
// Runs on worker nodes; the controller connects via RemoteWorker.
type Agent struct {
	pb.UnimplementedWorkerServiceServer
	worker    Worker
	gameStore *games.GameStore
	dataDir   string
	log       *slog.Logger
}

func NewAgent(w Worker, gameStore *games.GameStore, dataDir string, log *slog.Logger) *Agent {
	return &Agent{worker: w, gameStore: gameStore, dataDir: dataDir, log: log}
}

func (a *Agent) PullImage(ctx context.Context, req *pb.PullImageRequest) (*pb.PullImageResponse, error) {
	if err := a.worker.PullImage(ctx, req.Image); err != nil {
		return nil, err
	}
	return &pb.PullImageResponse{}, nil
}

func (a *Agent) CreateContainer(ctx context.Context, req *pb.CreateContainerRequest) (*pb.CreateContainerResponse, error) {
	opts := ContainerOptions{
		Name:          req.Name,
		Image:         req.Image,
		Env:           req.Env,
		VolumeName:    req.VolumeName,
		MemoryLimitMB: int(req.MemoryLimitMb),
		CPULimit:      req.CpuLimit,
		Entrypoint:    req.Entrypoint,
		User:          req.User,
		Binds:         req.Binds,
	}
	for _, p := range req.Ports {
		opts.Ports = append(opts.Ports, PortBinding{
			HostPort:      int(p.HostPort),
			ContainerPort: int(p.ContainerPort),
			Protocol:      p.Protocol,
		})
	}

	id, err := a.worker.CreateContainer(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &pb.CreateContainerResponse{ContainerId: id}, nil
}

func (a *Agent) StartContainer(ctx context.Context, req *pb.StartContainerRequest) (*pb.StartContainerResponse, error) {
	if err := a.worker.StartContainer(ctx, req.ContainerId); err != nil {
		return nil, err
	}
	return &pb.StartContainerResponse{}, nil
}

func (a *Agent) StopContainer(ctx context.Context, req *pb.StopContainerRequest) (*pb.StopContainerResponse, error) {
	if err := a.worker.StopContainer(ctx, req.ContainerId, int(req.TimeoutSeconds)); err != nil {
		return nil, err
	}
	return &pb.StopContainerResponse{}, nil
}

func (a *Agent) RemoveContainer(ctx context.Context, req *pb.RemoveContainerRequest) (*pb.RemoveContainerResponse, error) {
	if err := a.worker.RemoveContainer(ctx, req.ContainerId); err != nil {
		return nil, err
	}
	return &pb.RemoveContainerResponse{}, nil
}

func (a *Agent) InspectContainer(ctx context.Context, req *pb.InspectContainerRequest) (*pb.InspectContainerResponse, error) {
	info, err := a.worker.InspectContainer(ctx, req.ContainerId)
	if err != nil {
		return nil, err
	}
	return &pb.InspectContainerResponse{
		Id:             info.ID,
		State:          info.State,
		StartedAtUnix:  info.StartedAt.Unix(),
		ExitCode:       int32(info.ExitCode),
	}, nil
}

func (a *Agent) Exec(ctx context.Context, req *pb.ExecRequest) (*pb.ExecResponse, error) {
	exitCode, stdout, stderr, err := a.worker.Exec(ctx, req.ContainerId, req.Cmd)
	if err != nil {
		return nil, err
	}
	return &pb.ExecResponse{
		ExitCode: int32(exitCode),
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func (a *Agent) ContainerLogs(req *pb.ContainerLogsRequest, stream pb.WorkerService_ContainerLogsServer) error {
	reader, err := a.worker.ContainerLogs(stream.Context(), req.ContainerId, int(req.Tail), req.Follow)
	if err != nil {
		return err
	}
	defer reader.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := stream.Send(&pb.DataChunk{Data: chunk}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (a *Agent) ContainerStats(ctx context.Context, req *pb.ContainerStatsRequest) (*pb.ContainerStatsResponse, error) {
	stats, err := a.worker.ContainerStats(ctx, req.ContainerId)
	if err != nil {
		return nil, err
	}
	return &pb.ContainerStatsResponse{
		MemoryUsageMb: int32(stats.MemoryUsageMB),
		MemoryLimitMb: int32(stats.MemoryLimitMB),
		CpuPercent:    stats.CPUPercent,
	}, nil
}

func (a *Agent) VolumeSize(ctx context.Context, req *pb.VolumeSizeRequest) (*pb.VolumeSizeResponse, error) {
	size, err := a.worker.VolumeSize(ctx, req.VolumeName)
	if err != nil {
		return nil, err
	}
	return &pb.VolumeSizeResponse{SizeBytes: size}, nil
}

func (a *Agent) CreateVolume(ctx context.Context, req *pb.CreateVolumeRequest) (*pb.CreateVolumeResponse, error) {
	if err := a.worker.CreateVolume(ctx, req.Name); err != nil {
		return nil, err
	}
	return &pb.CreateVolumeResponse{}, nil
}

func (a *Agent) RemoveVolume(ctx context.Context, req *pb.RemoveVolumeRequest) (*pb.RemoveVolumeResponse, error) {
	if err := a.worker.RemoveVolume(ctx, req.Name); err != nil {
		return nil, err
	}
	return &pb.RemoveVolumeResponse{}, nil
}

func (a *Agent) ListFiles(ctx context.Context, req *pb.ListFilesRequest) (*pb.ListFilesResponse, error) {
	entries, err := a.worker.ListFiles(ctx, req.VolumeName, req.Path)
	if err != nil {
		return nil, err
	}
	resp := &pb.ListFilesResponse{}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, &pb.FileEntryMsg{
			Name:        e.Name,
			IsDir:       e.IsDir,
			Size:        e.Size,
			ModTimeUnix: e.ModTime.Unix(),
			Permissions: e.Permissions,
		})
	}
	return resp, nil
}

func (a *Agent) ReadFile(ctx context.Context, req *pb.ReadFileRequest) (*pb.ReadFileResponse, error) {
	content, err := a.worker.ReadFile(ctx, req.VolumeName, req.Path)
	if err != nil {
		return nil, err
	}
	return &pb.ReadFileResponse{Content: content}, nil
}

func (a *Agent) WriteFile(ctx context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	if err := a.worker.WriteFile(ctx, req.VolumeName, req.Path, req.Content, os.FileMode(req.Perm)); err != nil {
		return nil, err
	}
	return &pb.WriteFileResponse{}, nil
}

func (a *Agent) DeletePath(ctx context.Context, req *pb.DeletePathRequest) (*pb.DeletePathResponse, error) {
	if err := a.worker.DeletePath(ctx, req.VolumeName, req.Path); err != nil {
		return nil, err
	}
	return &pb.DeletePathResponse{}, nil
}

func (a *Agent) CreateDirectory(ctx context.Context, req *pb.CreateDirectoryRequest) (*pb.CreateDirectoryResponse, error) {
	if err := a.worker.CreateDirectory(ctx, req.VolumeName, req.Path); err != nil {
		return nil, err
	}
	return &pb.CreateDirectoryResponse{}, nil
}

func (a *Agent) RenamePath(ctx context.Context, req *pb.RenamePathRequest) (*pb.RenamePathResponse, error) {
	if err := a.worker.RenamePath(ctx, req.VolumeName, req.From, req.To); err != nil {
		return nil, err
	}
	return &pb.RenamePathResponse{}, nil
}

func (a *Agent) CopyFromContainer(ctx context.Context, req *pb.CopyFromContainerRequest) (*pb.CopyFromContainerResponse, error) {
	content, err := a.worker.CopyFromContainer(ctx, req.ContainerId, req.Path)
	if err != nil {
		return nil, err
	}
	return &pb.CopyFromContainerResponse{Content: content}, nil
}

func (a *Agent) CopyToContainer(ctx context.Context, req *pb.CopyToContainerRequest) (*pb.CopyToContainerResponse, error) {
	if err := a.worker.CopyToContainer(ctx, req.ContainerId, req.Path, req.Content); err != nil {
		return nil, err
	}
	return &pb.CopyToContainerResponse{}, nil
}

func (a *Agent) CopyDirFromContainer(req *pb.CopyDirFromContainerRequest, stream pb.WorkerService_CopyDirFromContainerServer) error {
	reader, err := a.worker.CopyDirFromContainer(stream.Context(), req.ContainerId, req.Path)
	if err != nil {
		return err
	}
	defer reader.Close()

	buf := make([]byte, 64*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := stream.Send(&pb.DataChunk{Data: chunk}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (a *Agent) CopyTarToContainer(stream pb.WorkerService_CopyTarToContainerServer) error {
	var containerID, destPath string
	var buf bytes.Buffer

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if containerID == "" {
			containerID = msg.ContainerId
			destPath = msg.DestPath
		}
		buf.Write(msg.Data)
	}

	if containerID == "" {
		return fmt.Errorf("no messages received")
	}

	if err := a.worker.CopyTarToContainer(stream.Context(), containerID, destPath, &buf); err != nil {
		return err
	}
	return stream.SendAndClose(&pb.CopyTarToContainerResponse{})
}

func (a *Agent) BackupVolume(req *pb.BackupVolumeRequest, stream pb.WorkerService_BackupVolumeServer) error {
	reader, err := a.worker.BackupVolume(stream.Context(), req.VolumeName)
	if err != nil {
		return err
	}
	defer reader.Close()

	buf := make([]byte, 64*1024)
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			if err := stream.Send(&pb.DataChunk{Data: chunk}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (a *Agent) RestoreVolume(stream pb.WorkerService_RestoreVolumeServer) error {
	var volumeName string
	var buf bytes.Buffer

	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if volumeName == "" {
			volumeName = msg.VolumeName
		}
		buf.Write(msg.Data)
	}

	if volumeName == "" {
		return fmt.Errorf("no messages received")
	}

	if err := a.worker.RestoreVolume(stream.Context(), volumeName, &buf); err != nil {
		return err
	}
	return stream.SendAndClose(&pb.RestoreVolumeResponse{})
}

func (a *Agent) WatchEvents(req *pb.WatchEventsRequest, stream pb.WorkerService_WatchEventsServer) error {
	events, errs := a.worker.WatchEvents(stream.Context())

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(&pb.ContainerEventMsg{
				ContainerId:   event.ContainerID,
				ContainerName: event.ContainerName,
				Action:        event.Action,
			}); err != nil {
				return err
			}
		case err, ok := <-errs:
			if !ok {
				return nil
			}
			return err
		}
	}
}

func (a *Agent) PrepareGameScripts(ctx context.Context, req *pb.PrepareGameScriptsRequest) (*pb.PrepareGameScriptsResponse, error) {
	gsDir := filepath.Join(a.dataDir, "gameservers", req.GameserverId)
	if err := a.gameStore.ExtractScripts(req.GameId, gsDir); err != nil {
		return nil, fmt.Errorf("extracting scripts: %w", err)
	}

	resp := &pb.PrepareGameScriptsResponse{
		ScriptDir: filepath.Join(gsDir, "scripts"),
	}

	defaultsDir := filepath.Join(gsDir, "defaults")
	if _, err := os.Stat(defaultsDir); err == nil {
		resp.DefaultsDir = defaultsDir
	}

	a.log.Debug("prepared game scripts", "game_id", req.GameId, "gameserver_id", req.GameserverId)
	return resp, nil
}

func (a *Agent) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	a.log.Debug("heartbeat received",
		"worker_id", req.WorkerId,
		"cpu_cores", req.CpuCores,
		"memory_total_mb", req.MemoryTotalMb,
		"memory_available_mb", req.MemoryAvailableMb,
	)
	_ = time.Now() // placeholder for future last-seen tracking
	return &pb.HeartbeatResponse{Accepted: true}, nil
}
