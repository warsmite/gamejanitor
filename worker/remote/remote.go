package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/warsmite/gamejanitor/worker"
	pb "github.com/warsmite/gamejanitor/worker/proto"
	"google.golang.org/grpc"
)

// RemoteWorker implements the Worker interface by making gRPC calls to a worker agent.
// Used by the controller to communicate with remote workers.
type RemoteWorker struct {
	conn   *grpc.ClientConn
	client pb.WorkerServiceClient
	nodeID string
}

func New(conn *grpc.ClientConn, nodeID string) *RemoteWorker {
	return &RemoteWorker{
		conn:   conn,
		client: pb.NewWorkerServiceClient(conn),
		nodeID: nodeID,
	}
}

func (w *RemoteWorker) NodeID() string { return w.nodeID }

func (w *RemoteWorker) PullImage(ctx context.Context, image string, onProgress func(worker.PullProgress)) error {
	stream, err := w.client.PullImage(ctx, &pb.PullImageRequest{Image: image})
	if err != nil {
		return err
	}

	for {
		msg, err := stream.Recv()
		if err != nil {
			return nil // stream ended = success
		}
		if msg.Completed {
			return nil
		}
		if onProgress != nil {
			onProgress(worker.PullProgress{
				CompletedBytes:  msg.CompletedBytes,
				TotalBytes:      msg.TotalBytes,
				CompletedLayers: int(msg.CompletedLayers),
				TotalLayers:     int(msg.TotalLayers),
			})
		}
	}
}

func (w *RemoteWorker) CreateInstance(ctx context.Context, opts worker.InstanceOptions) (string, error) {
	req := &pb.CreateInstanceRequest{
		Name:          opts.Name,
		Image:         opts.Image,
		Env:           opts.Env,
		VolumeName:    opts.VolumeName,
		MemoryLimitMb: int32(opts.MemoryLimitMB),
		CpuLimit:      opts.CPULimit,
		CpuEnforced:   opts.CPUEnforced,
		Entrypoint:    opts.Entrypoint,
		User:          opts.User,
		Binds:         opts.Binds,
	}
	for _, p := range opts.Ports {
		req.Ports = append(req.Ports, &pb.PortBinding{
			HostPort:      int32(p.HostPort),
			InstancePort: int32(p.InstancePort),
			Protocol:      p.Protocol,
		})
	}

	resp, err := w.client.CreateInstance(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.InstanceId, nil
}

func (w *RemoteWorker) StartInstance(ctx context.Context, id string, readyPattern string) error {
	_, err := w.client.StartInstance(ctx, &pb.StartInstanceRequest{
		InstanceId:   id,
		ReadyPattern: readyPattern,
	})
	return err
}

func (w *RemoteWorker) StopInstance(ctx context.Context, id string, timeoutSeconds int) error {
	_, err := w.client.StopInstance(ctx, &pb.StopInstanceRequest{
		InstanceId:    id,
		TimeoutSeconds: int32(timeoutSeconds),
	})
	return err
}

func (w *RemoteWorker) RemoveInstance(ctx context.Context, id string) error {
	_, err := w.client.RemoveInstance(ctx, &pb.RemoveInstanceRequest{InstanceId: id})
	return err
}

func (w *RemoteWorker) InspectInstance(ctx context.Context, id string) (*worker.InstanceInfo, error) {
	resp, err := w.client.InspectInstance(ctx, &pb.InspectInstanceRequest{InstanceId: id})
	if err != nil {
		return nil, err
	}
	return &worker.InstanceInfo{
		ID:        resp.Id,
		State:     resp.State,
		StartedAt: time.Unix(resp.StartedAtUnix, 0),
		ExitCode:  int(resp.ExitCode),
	}, nil
}

func (w *RemoteWorker) Exec(ctx context.Context, instanceID string, cmd []string) (int, string, string, error) {
	resp, err := w.client.Exec(ctx, &pb.ExecRequest{
		InstanceId: instanceID,
		Cmd:         cmd,
	})
	if err != nil {
		return 0, "", "", err
	}
	return int(resp.ExitCode), resp.Stdout, resp.Stderr, nil
}

func (w *RemoteWorker) InstanceLogs(ctx context.Context, instanceID string, tail int, follow bool) (io.ReadCloser, error) {
	stream, err := w.client.InstanceLogs(ctx, &pb.InstanceLogsRequest{
		InstanceId: instanceID,
		Tail:        int32(tail),
		Follow:      follow,
	})
	if err != nil {
		return nil, err
	}
	return &grpcStreamReader{stream: stream}, nil
}

func (w *RemoteWorker) InstanceStats(ctx context.Context, instanceID string) (*worker.InstanceStats, error) {
	resp, err := w.client.InstanceStats(ctx, &pb.InstanceStatsRequest{InstanceId: instanceID})
	if err != nil {
		return nil, err
	}
	return &worker.InstanceStats{
		MemoryUsageMB: int(resp.MemoryUsageMb),
		MemoryLimitMB: int(resp.MemoryLimitMb),
		CPUPercent:    resp.CpuPercent,
		NetRxBytes:    resp.NetRxBytes,
		NetTxBytes:    resp.NetTxBytes,
	}, nil
}

func (w *RemoteWorker) VolumeSize(ctx context.Context, volumeName string) (int64, error) {
	resp, err := w.client.VolumeSize(ctx, &pb.VolumeSizeRequest{VolumeName: volumeName})
	if err != nil {
		return 0, err
	}
	return resp.SizeBytes, nil
}

func (w *RemoteWorker) CreateVolume(ctx context.Context, name string) error {
	_, err := w.client.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: name})
	return err
}

func (w *RemoteWorker) RemoveVolume(ctx context.Context, name string) error {
	_, err := w.client.RemoveVolume(ctx, &pb.RemoveVolumeRequest{Name: name})
	return err
}

func (w *RemoteWorker) ListFiles(ctx context.Context, volumeName string, path string) ([]worker.FileEntry, error) {
	resp, err := w.client.ListFiles(ctx, &pb.ListFilesRequest{
		VolumeName: volumeName,
		Path:       path,
	})
	if err != nil {
		return nil, err
	}
	entries := make([]worker.FileEntry, len(resp.Entries))
	for i, e := range resp.Entries {
		entries[i] = worker.FileEntry{
			Name:        e.Name,
			IsDir:       e.IsDir,
			Size:        e.Size,
			ModTime:     time.Unix(e.ModTimeUnix, 0),
			Permissions: e.Permissions,
		}
	}
	return entries, nil
}

func (w *RemoteWorker) ReadFile(ctx context.Context, volumeName string, path string) ([]byte, error) {
	resp, err := w.client.ReadFile(ctx, &pb.ReadFileRequest{
		VolumeName: volumeName,
		Path:       path,
	})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

// OpenFile falls back to ReadFile for remote workers — the file bytes cross gRPC
// as a single message. Streaming file downloads over gRPC would require a new
// server-streaming RPC, which can be added later for large file support.
func (w *RemoteWorker) OpenFile(ctx context.Context, volumeName string, path string) (io.ReadCloser, int64, error) {
	data, err := w.ReadFile(ctx, volumeName, path)
	if err != nil {
		return nil, 0, err
	}
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

func (w *RemoteWorker) WriteFile(ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	_, err := w.client.WriteFile(ctx, &pb.WriteFileRequest{
		VolumeName: volumeName,
		Path:       path,
		Content:    content,
		Perm:       uint32(perm),
	})
	return err
}

func (w *RemoteWorker) WriteFileStream(ctx context.Context, volumeName string, path string, reader io.Reader, perm os.FileMode) error {
	stream, err := w.client.WriteFileStream(ctx)
	if err != nil {
		return fmt.Errorf("opening WriteFileStream to %s: %w", w.nodeID, err)
	}

	buf := make([]byte, 64*1024)
	first := true
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			msg := &pb.WriteFileStreamRequest{Data: buf[:n]}
			if first {
				msg.VolumeName = volumeName
				msg.Path = path
				msg.Perm = uint32(perm)
				first = false
			}
			if err := stream.Send(msg); err != nil {
				return fmt.Errorf("sending chunk to %s: %w", w.nodeID, err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("reading file for %s: %w", w.nodeID, readErr)
		}
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("closing WriteFileStream to %s: %w", w.nodeID, err)
	}
	return nil
}

func (w *RemoteWorker) DownloadFile(ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	_, err := w.client.DownloadFile(ctx, &pb.DownloadFileRequest{
		VolumeName:   volumeName,
		Url:          url,
		DestPath:     destPath,
		ExpectedHash: expectedHash,
		MaxBytes:     maxBytes,
	})
	return err
}

func (w *RemoteWorker) DeletePath(ctx context.Context, volumeName string, path string) error {
	_, err := w.client.DeletePath(ctx, &pb.DeletePathRequest{
		VolumeName: volumeName,
		Path:       path,
	})
	return err
}

func (w *RemoteWorker) CreateDirectory(ctx context.Context, volumeName string, path string) error {
	_, err := w.client.CreateDirectory(ctx, &pb.CreateDirectoryRequest{
		VolumeName: volumeName,
		Path:       path,
	})
	return err
}

func (w *RemoteWorker) RenamePath(ctx context.Context, volumeName string, from string, to string) error {
	_, err := w.client.RenamePath(ctx, &pb.RenamePathRequest{
		VolumeName: volumeName,
		From:       from,
		To:         to,
	})
	return err
}

func (w *RemoteWorker) CopyFromInstance(ctx context.Context, instanceID string, path string) ([]byte, error) {
	resp, err := w.client.CopyFromInstance(ctx, &pb.CopyFromInstanceRequest{
		InstanceId: instanceID,
		Path:        path,
	})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

func (w *RemoteWorker) CopyToInstance(ctx context.Context, instanceID string, path string, content []byte) error {
	_, err := w.client.CopyToInstance(ctx, &pb.CopyToInstanceRequest{
		InstanceId: instanceID,
		Path:        path,
		Content:     content,
	})
	return err
}

func (w *RemoteWorker) CopyDirFromInstance(ctx context.Context, instanceID string, path string) (io.ReadCloser, error) {
	stream, err := w.client.CopyDirFromInstance(ctx, &pb.CopyDirFromInstanceRequest{
		InstanceId: instanceID,
		Path:        path,
	})
	if err != nil {
		return nil, err
	}
	return &grpcStreamReader{stream: stream}, nil
}

func (w *RemoteWorker) CopyTarToInstance(ctx context.Context, instanceID string, destPath string, content io.Reader) error {
	stream, err := w.client.CopyTarToInstance(ctx)
	if err != nil {
		return err
	}

	buf := make([]byte, 64*1024)
	first := true
	for {
		n, readErr := content.Read(buf)
		if n > 0 {
			msg := &pb.CopyTarToInstanceRequest{Data: buf[:n]}
			if first {
				msg.InstanceId = instanceID
				msg.DestPath = destPath
				first = false
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	_, err = stream.CloseAndRecv()
	return err
}

func (w *RemoteWorker) WatchInstanceStates(ctx context.Context) (<-chan worker.InstanceStateUpdate, <-chan error) {
	updates := make(chan worker.InstanceStateUpdate, 64)
	errs := make(chan error, 1)

	stream, err := w.client.WatchInstanceStates(ctx, &pb.WatchInstanceStatesRequest{})
	if err != nil {
		errs <- err
		close(updates)
		close(errs)
		return updates, errs
	}

	go func() {
		defer close(updates)
		defer close(errs)
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					errs <- err
				}
				return
			}
			select {
			case updates <- protoToWorkerState(msg):
			case <-ctx.Done():
				return
			}
		}
	}()

	return updates, errs
}

func (w *RemoteWorker) GetAllInstanceStates(ctx context.Context) ([]worker.InstanceStateUpdate, error) {
	resp, err := w.client.GetAllInstanceStates(ctx, &pb.GetAllInstanceStatesRequest{})
	if err != nil {
		return nil, err
	}
	states := make([]worker.InstanceStateUpdate, len(resp.Instances))
	for i, inst := range resp.Instances {
		states[i] = protoToWorkerState(inst)
	}
	return states, nil
}

func protoToWorkerState(msg *pb.InstanceStateUpdate) worker.InstanceStateUpdate {
	var readyAt time.Time
	if msg.ReadyAtUnix != 0 {
		readyAt = time.Unix(msg.ReadyAtUnix, 0)
	}
	return worker.InstanceStateUpdate{
		InstanceID:   msg.InstanceId,
		InstanceName: msg.InstanceName,
		State:        protoToInstanceState(msg.State),
		Ready:        msg.Ready,
		ReadyAt:      readyAt,
		ExitCode:     int(msg.ExitCode),
		StartedAt:    time.Unix(msg.StartedAtUnix, 0),
		ExitedAt:     time.Unix(msg.ExitedAtUnix, 0),
		Installed:    msg.Installed,
	}
}

func protoToInstanceState(s pb.InstanceState) worker.InstanceState {
	switch s {
	case pb.InstanceState_INSTANCE_CREATED:
		return worker.StateCreated
	case pb.InstanceState_INSTANCE_RUNNING:
		return worker.StateRunning
	case pb.InstanceState_INSTANCE_EXITED:
		return worker.StateExited
	default:
		return worker.StateCreated
	}
}

// grpcStreamReader wraps a gRPC DataChunk stream as an io.ReadCloser.
type grpcStreamReader struct {
	stream interface {
		Recv() (*pb.DataChunk, error)
	}
	buf bytes.Buffer
}

func (r *grpcStreamReader) Read(p []byte) (int, error) {
	if r.buf.Len() > 0 {
		return r.buf.Read(p)
	}
	chunk, err := r.stream.Recv()
	if err != nil {
		return 0, err
	}
	r.buf.Write(chunk.Data)
	return r.buf.Read(p)
}

func (r *grpcStreamReader) Close() error {
	// gRPC streams are closed when the context is cancelled or server finishes
	return nil
}

func (w *RemoteWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	resp, err := w.client.PrepareGameScripts(ctx, &pb.PrepareGameScriptsRequest{
		GameId:       gameID,
		GameserverId: gameserverID,
	})
	if err != nil {
		return "", "", err
	}
	return resp.ScriptDir, resp.DefaultsDir, nil
}

func (w *RemoteWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	stream, err := w.client.EnsureDepot(ctx, &pb.EnsureDepotRequest{
		AppId:        appID,
		Branch:       branch,
		AccountName:  accountName,
		RefreshToken: refreshToken,
	})
	if err != nil {
		return nil, err
	}

	var result *worker.DepotResult
	for {
		msg, err := stream.Recv()
		if err != nil {
			if result != nil {
				return result, nil
			}
			return nil, err
		}

		// Final message has depot_dir set
		if msg.DepotDir != "" {
			result = &worker.DepotResult{
				DepotDir:        msg.DepotDir,
				Cached:          msg.Cached,
				BytesDownloaded: msg.BytesDownloaded,
			}
		} else if onProgress != nil {
			onProgress(worker.DepotProgress{
				CompletedBytes:  msg.CompletedBytes,
				TotalBytes:      msg.TotalBytes,
				CompletedChunks: int(msg.CompletedChunks),
				TotalChunks:     int(msg.TotalChunks),
			})
		}
	}
}

func (w *RemoteWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	_, err := w.client.DownloadWorkshopItem(ctx, &pb.DownloadWorkshopItemRequest{
		VolumeName:    volumeName,
		AppId:         appID,
		HcontentFile:  hcontentFile,
		InstallPath:   installPath,
	})
	return err
}

func (w *RemoteWorker) Sendbeat(ctx context.Context, req *pb.HeartbeatRequest) error {
	_, err := w.client.Heartbeat(ctx, req)
	return err
}

func (w *RemoteWorker) Close() error {
	if w.conn != nil {
		return w.conn.Close()
	}
	return nil
}

// grpcLogsStream and grpcDirStream share the same interface so grpcStreamReader works for both.
var _ io.ReadCloser = (*grpcStreamReader)(nil)
var _ fmt.Stringer = (*RemoteWorker)(nil)

func (w *RemoteWorker) String() string {
	return fmt.Sprintf("RemoteWorker(%s)", w.nodeID)
}

func (w *RemoteWorker) BackupVolume(ctx context.Context, volumeName string) (io.ReadCloser, error) {
	stream, err := w.client.BackupVolume(ctx, &pb.BackupVolumeRequest{VolumeName: volumeName})
	if err != nil {
		return nil, err
	}
	return &grpcStreamReader{stream: stream}, nil
}

func (w *RemoteWorker) RestoreVolume(ctx context.Context, volumeName string, tarStream io.Reader) error {
	stream, err := w.client.RestoreVolume(ctx)
	if err != nil {
		return err
	}

	buf := make([]byte, 64*1024)
	first := true
	for {
		n, readErr := tarStream.Read(buf)
		if n > 0 {
			msg := &pb.RestoreVolumeRequest{Data: buf[:n]}
			if first {
				msg.VolumeName = volumeName
				first = false
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	_, err = stream.CloseAndRecv()
	return err
}

func (w *RemoteWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	_, err := w.client.CopyDepotToVolume(ctx, &pb.CopyDepotToVolumeRequest{
		DepotDir:   depotDir,
		VolumeName: volumeName,
	})
	return err
}

func (w *RemoteWorker) ListGameserverInstances(ctx context.Context) ([]worker.GameserverInstance, error) {
	resp, err := w.client.ListGameserverInstances(ctx, &pb.ListGameserverInstancesRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing gameserver instances on %s: %w", w.nodeID, err)
	}
	var result []worker.GameserverInstance
	for _, c := range resp.Instances {
		result = append(result, worker.GameserverInstance{
			InstanceID:   c.InstanceId,
			InstanceName: c.InstanceName,
			GameserverID:  c.GameserverId,
			State:         c.State,
		})
	}
	return result, nil
}
