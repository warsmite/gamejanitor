package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/worker"
	pb "github.com/warsmite/gamejanitor/worker/proto"
)

// Agent wraps a LocalWorker and exposes it as a gRPC service.
// Runs on worker nodes; the controller connects via RemoteWorker.
type Agent struct {
	pb.UnimplementedWorkerServiceServer
	worker    worker.Worker
	gameStore *games.GameStore
	dataDir   string
	log       *slog.Logger
}

func New(w worker.Worker, gameStore *games.GameStore, dataDir string, log *slog.Logger) *Agent {
	return &Agent{worker: w, gameStore: gameStore, dataDir: dataDir, log: log}
}

func (a *Agent) PullImage(req *pb.PullImageRequest, stream pb.WorkerService_PullImageServer) error {
	err := a.worker.PullImage(stream.Context(), req.Image, func(p worker.PullProgress) {
		stream.Send(&pb.PullImageProgress{
			CompletedBytes:  p.CompletedBytes,
			TotalBytes:      p.TotalBytes,
			CompletedLayers: int32(p.CompletedLayers),
			TotalLayers:     int32(p.TotalLayers),
		})
	})
	if err != nil {
		return err
	}
	return stream.Send(&pb.PullImageProgress{Completed: true})
}

func (a *Agent) CreateInstance(ctx context.Context, req *pb.CreateInstanceRequest) (*pb.CreateInstanceResponse, error) {
	opts := worker.InstanceOptions{
		Name:          req.Name,
		Image:         req.Image,
		Env:           req.Env,
		VolumeName:    req.VolumeName,
		MemoryLimitMB: int(req.MemoryLimitMb),
		CPULimit:      req.CpuLimit,
		CPUEnforced:   req.CpuEnforced,
		Entrypoint:    req.Entrypoint,
		User:          req.User,
		Binds:         req.Binds,
	}
	for _, p := range req.Ports {
		opts.Ports = append(opts.Ports, worker.PortBinding{
			HostPort:      int(p.HostPort),
			InstancePort: int(p.InstancePort),
			Protocol:      p.Protocol,
		})
	}

	id, err := a.worker.CreateInstance(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &pb.CreateInstanceResponse{InstanceId: id}, nil
}

func (a *Agent) StartInstance(ctx context.Context, req *pb.StartInstanceRequest) (*pb.StartInstanceResponse, error) {
	if err := a.worker.StartInstance(ctx, req.InstanceId, req.ReadyPattern); err != nil {
		return nil, err
	}
	return &pb.StartInstanceResponse{}, nil
}

func (a *Agent) StopInstance(ctx context.Context, req *pb.StopInstanceRequest) (*pb.StopInstanceResponse, error) {
	if err := a.worker.StopInstance(ctx, req.InstanceId, int(req.TimeoutSeconds)); err != nil {
		return nil, err
	}
	return &pb.StopInstanceResponse{}, nil
}

func (a *Agent) RemoveInstance(ctx context.Context, req *pb.RemoveInstanceRequest) (*pb.RemoveInstanceResponse, error) {
	if err := a.worker.RemoveInstance(ctx, req.InstanceId); err != nil {
		return nil, err
	}
	return &pb.RemoveInstanceResponse{}, nil
}

func (a *Agent) InspectInstance(ctx context.Context, req *pb.InspectInstanceRequest) (*pb.InspectInstanceResponse, error) {
	info, err := a.worker.InspectInstance(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return &pb.InspectInstanceResponse{
		Id:             info.ID,
		State:          info.State,
		StartedAtUnix:  info.StartedAt.Unix(),
		ExitCode:       int32(info.ExitCode),
	}, nil
}

func (a *Agent) Exec(ctx context.Context, req *pb.ExecRequest) (*pb.ExecResponse, error) {
	exitCode, stdout, stderr, err := a.worker.Exec(ctx, req.InstanceId, req.Cmd)
	if err != nil {
		return nil, err
	}
	return &pb.ExecResponse{
		ExitCode: int32(exitCode),
		Stdout:   stdout,
		Stderr:   stderr,
	}, nil
}

func (a *Agent) InstanceLogs(req *pb.InstanceLogsRequest, stream pb.WorkerService_InstanceLogsServer) error {
	reader, err := a.worker.InstanceLogs(stream.Context(), req.InstanceId, int(req.Tail), req.Follow)
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

func (a *Agent) InstanceStats(ctx context.Context, req *pb.InstanceStatsRequest) (*pb.InstanceStatsResponse, error) {
	stats, err := a.worker.InstanceStats(ctx, req.InstanceId)
	if err != nil {
		return nil, err
	}
	return &pb.InstanceStatsResponse{
		MemoryUsageMb: int32(stats.MemoryUsageMB),
		MemoryLimitMb: int32(stats.MemoryLimitMB),
		CpuPercent:    stats.CPUPercent,
		NetRxBytes:    stats.NetRxBytes,
		NetTxBytes:    stats.NetTxBytes,
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

func (a *Agent) WriteFileStream(stream pb.WorkerService_WriteFileStreamServer) error {
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receiving first chunk: %w", err)
	}
	volumeName := first.VolumeName
	path := first.Path
	perm := os.FileMode(first.Perm)
	if volumeName == "" || path == "" {
		return fmt.Errorf("first message missing volume_name or path")
	}

	pr, pw := io.Pipe()
	var writeErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		writeErr = a.worker.WriteFileStream(stream.Context(), volumeName, path, pr, perm)
	}()

	// Write data from first message
	if len(first.Data) > 0 {
		if _, err := pw.Write(first.Data); err != nil {
			pw.Close()
			<-done
			return writeErr
		}
	}
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			pw.Close()
			<-done
			if writeErr != nil {
				return writeErr
			}
			return stream.SendAndClose(&pb.WriteFileStreamResponse{})
		}
		if err != nil {
			pw.CloseWithError(err)
			<-done
			return err
		}
		if _, err := pw.Write(msg.Data); err != nil {
			pw.Close()
			<-done
			return writeErr
		}
	}
}

func (a *Agent) DownloadFile(ctx context.Context, req *pb.DownloadFileRequest) (*pb.DownloadFileResponse, error) {
	if err := a.worker.DownloadFile(ctx, req.VolumeName, req.Url, req.DestPath, req.ExpectedHash, req.MaxBytes); err != nil {
		return nil, err
	}
	return &pb.DownloadFileResponse{}, nil
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

func (a *Agent) CopyFromInstance(ctx context.Context, req *pb.CopyFromInstanceRequest) (*pb.CopyFromInstanceResponse, error) {
	content, err := a.worker.CopyFromInstance(ctx, req.InstanceId, req.Path)
	if err != nil {
		return nil, err
	}
	return &pb.CopyFromInstanceResponse{Content: content}, nil
}

func (a *Agent) CopyToInstance(ctx context.Context, req *pb.CopyToInstanceRequest) (*pb.CopyToInstanceResponse, error) {
	if err := a.worker.CopyToInstance(ctx, req.InstanceId, req.Path, req.Content); err != nil {
		return nil, err
	}
	return &pb.CopyToInstanceResponse{}, nil
}

func (a *Agent) CopyDirFromInstance(req *pb.CopyDirFromInstanceRequest, stream pb.WorkerService_CopyDirFromInstanceServer) error {
	reader, err := a.worker.CopyDirFromInstance(stream.Context(), req.InstanceId, req.Path)
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

func (a *Agent) CopyTarToInstance(stream pb.WorkerService_CopyTarToInstanceServer) error {
	// Read the first message to get instance ID and dest path
	first, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("no messages received: %w", err)
	}
	instanceID := first.InstanceId
	destPath := first.DestPath
	if instanceID == "" {
		return fmt.Errorf("first message missing instance_id")
	}

	// Stream remaining chunks through a pipe to avoid buffering the entire tar
	pr, pw := io.Pipe()
	var copyErr error
	go func() {
		defer pw.Close()
		// Write data from the first message
		if len(first.Data) > 0 {
			if _, err := pw.Write(first.Data); err != nil {
				return
			}
		}
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			if _, err := pw.Write(msg.Data); err != nil {
				return
			}
		}
	}()

	copyErr = a.worker.CopyTarToInstance(stream.Context(), instanceID, destPath, pr)
	if copyErr != nil {
		return copyErr
	}
	return stream.SendAndClose(&pb.CopyTarToInstanceResponse{})
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
	// Stream directly to the restore function via a pipe — no buffering.
	// Large volumes (3+ GB) would OOM the worker if buffered in memory.
	pr, pw := io.Pipe()
	var volumeName string
	var recvErr error

	go func() {
		defer pw.Close()
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				recvErr = err
				pw.CloseWithError(err)
				return
			}
			if volumeName == "" {
				volumeName = msg.VolumeName
			}
			if _, err := pw.Write(msg.Data); err != nil {
				return
			}
		}
	}()

	// Wait for the first message to get the volume name
	// The pipe won't have data until Recv returns, so read a small amount first
	firstBuf := make([]byte, 1)
	if _, err := pr.Read(firstBuf); err != nil {
		if recvErr != nil {
			return recvErr
		}
		return fmt.Errorf("no data received")
	}
	if volumeName == "" {
		return fmt.Errorf("no volume name received")
	}

	// Prepend the first byte back via a MultiReader
	restoreReader := io.MultiReader(bytes.NewReader(firstBuf), pr)

	if err := a.worker.RestoreVolume(stream.Context(), volumeName, restoreReader); err != nil {
		return err
	}
	if recvErr != nil {
		return recvErr
	}
	return stream.SendAndClose(&pb.RestoreVolumeResponse{})
}

func (a *Agent) WatchInstanceStates(req *pb.WatchInstanceStatesRequest, stream pb.WorkerService_WatchInstanceStatesServer) error {
	updates, errs := a.worker.WatchInstanceStates(stream.Context())

	for {
		select {
		case <-stream.Context().Done():
			return nil
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if err := stream.Send(workerStateToProto(update)); err != nil {
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

func (a *Agent) GetAllInstanceStates(ctx context.Context, req *pb.GetAllInstanceStatesRequest) (*pb.GetAllInstanceStatesResponse, error) {
	states, err := a.worker.GetAllInstanceStates(ctx)
	if err != nil {
		return nil, err
	}
	resp := &pb.GetAllInstanceStatesResponse{}
	for _, s := range states {
		resp.Instances = append(resp.Instances, workerStateToProto(s))
	}
	return resp, nil
}

func workerStateToProto(u worker.InstanceStateUpdate) *pb.InstanceStateUpdate {
	var startedAt, exitedAt, readyAt int64
	if !u.StartedAt.IsZero() {
		startedAt = u.StartedAt.Unix()
	}
	if !u.ExitedAt.IsZero() {
		exitedAt = u.ExitedAt.Unix()
	}
	if !u.ReadyAt.IsZero() {
		readyAt = u.ReadyAt.Unix()
	}
	return &pb.InstanceStateUpdate{
		InstanceId:    u.InstanceID,
		InstanceName:  u.InstanceName,
		State:         mapInstanceState(u.State),
		Ready:         u.Ready,
		ReadyAtUnix:   readyAt,
		ExitCode:      int32(u.ExitCode),
		StartedAtUnix: startedAt,
		ExitedAtUnix:  exitedAt,
		Installed:     u.Installed,
	}
}

func mapInstanceState(s worker.InstanceState) pb.InstanceState {
	switch s {
	case worker.StateCreated:
		return pb.InstanceState_INSTANCE_CREATED
	case worker.StateRunning:
		return pb.InstanceState_INSTANCE_RUNNING
	case worker.StateExited:
		return pb.InstanceState_INSTANCE_EXITED
	default:
		return pb.InstanceState_INSTANCE_CREATED
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

	a.log.Debug("prepared game scripts", "game_id", req.GameId, "gameserver", req.GameserverId)
	return resp, nil
}

func (a *Agent) EnsureDepot(req *pb.EnsureDepotRequest, stream pb.WorkerService_EnsureDepotServer) error {
	var lastSent time.Time
	onProgress := func(p worker.DepotProgress) {
		now := time.Now()
		if now.Sub(lastSent) < 200*time.Millisecond {
			return
		}
		lastSent = now
		stream.Send(&pb.EnsureDepotProgress{
			CompletedBytes:  p.CompletedBytes,
			TotalBytes:      p.TotalBytes,
			CompletedChunks: int32(p.CompletedChunks),
			TotalChunks:     int32(p.TotalChunks),
		})
	}

	result, err := worker.EnsureDepot(stream.Context(), a.dataDir, a.log, req.AppId, req.Branch, req.AccountName, req.RefreshToken, onProgress)
	if err != nil {
		return err
	}

	// Final message with result
	return stream.Send(&pb.EnsureDepotProgress{
		DepotDir:        result.DepotDir,
		Cached:          result.Cached,
		BytesDownloaded: result.BytesDownloaded,
	})
}

func (a *Agent) DownloadWorkshopItem(ctx context.Context, req *pb.DownloadWorkshopItemRequest) (*pb.DownloadWorkshopItemResponse, error) {
	if err := a.worker.DownloadWorkshopItem(ctx, req.VolumeName, req.AppId, req.HcontentFile, req.InstallPath); err != nil {
		return nil, err
	}
	return &pb.DownloadWorkshopItemResponse{}, nil
}

func (a *Agent) CopyDepotToVolume(ctx context.Context, req *pb.CopyDepotToVolumeRequest) (*pb.CopyDepotToVolumeResponse, error) {
	if err := a.worker.CopyDepotToVolume(ctx, req.DepotDir, req.VolumeName); err != nil {
		return nil, err
	}
	return &pb.CopyDepotToVolumeResponse{}, nil
}

func (a *Agent) ListGameserverInstances(ctx context.Context, req *pb.ListGameserverInstancesRequest) (*pb.ListGameserverInstancesResponse, error) {
	instances, err := a.worker.ListGameserverInstances(ctx)
	if err != nil {
		return nil, err
	}
	var pbInstances []*pb.GameserverInstance
	for _, c := range instances {
		pbInstances = append(pbInstances, &pb.GameserverInstance{
			InstanceId:   c.InstanceID,
			InstanceName: c.InstanceName,
			GameserverId:  c.GameserverID,
			State:         c.State,
		})
	}
	return &pb.ListGameserverInstancesResponse{Instances: pbInstances}, nil
}

func (a *Agent) Heartbeat(ctx context.Context, req *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	a.log.Debug("heartbeat received",
		"worker", req.WorkerId,
		"cpu_cores", req.CpuCores,
		"memory_total_mb", req.MemoryTotalMb,
		"memory_available_mb", req.MemoryAvailableMb,
	)
	return &pb.HeartbeatResponse{Accepted: true}, nil
}
