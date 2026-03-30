package remote

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/warsmite/gamejanitor/worker"
	"github.com/warsmite/gamejanitor/worker/pb"
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

func (w *RemoteWorker) PullImage(ctx context.Context, image string) error {
	_, err := w.client.PullImage(ctx, &pb.PullImageRequest{Image: image})
	return err
}

func (w *RemoteWorker) CreateContainer(ctx context.Context, opts worker.ContainerOptions) (string, error) {
	req := &pb.CreateContainerRequest{
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
			ContainerPort: int32(p.ContainerPort),
			Protocol:      p.Protocol,
		})
	}

	resp, err := w.client.CreateContainer(ctx, req)
	if err != nil {
		return "", err
	}
	return resp.ContainerId, nil
}

func (w *RemoteWorker) StartContainer(ctx context.Context, id string) error {
	_, err := w.client.StartContainer(ctx, &pb.StartContainerRequest{ContainerId: id})
	return err
}

func (w *RemoteWorker) StopContainer(ctx context.Context, id string, timeoutSeconds int) error {
	_, err := w.client.StopContainer(ctx, &pb.StopContainerRequest{
		ContainerId:    id,
		TimeoutSeconds: int32(timeoutSeconds),
	})
	return err
}

func (w *RemoteWorker) RemoveContainer(ctx context.Context, id string) error {
	_, err := w.client.RemoveContainer(ctx, &pb.RemoveContainerRequest{ContainerId: id})
	return err
}

func (w *RemoteWorker) InspectContainer(ctx context.Context, id string) (*worker.ContainerInfo, error) {
	resp, err := w.client.InspectContainer(ctx, &pb.InspectContainerRequest{ContainerId: id})
	if err != nil {
		return nil, err
	}
	return &worker.ContainerInfo{
		ID:        resp.Id,
		State:     resp.State,
		StartedAt: time.Unix(resp.StartedAtUnix, 0),
		ExitCode:  int(resp.ExitCode),
	}, nil
}

func (w *RemoteWorker) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	resp, err := w.client.Exec(ctx, &pb.ExecRequest{
		ContainerId: containerID,
		Cmd:         cmd,
	})
	if err != nil {
		return 0, "", "", err
	}
	return int(resp.ExitCode), resp.Stdout, resp.Stderr, nil
}

func (w *RemoteWorker) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	stream, err := w.client.ContainerLogs(ctx, &pb.ContainerLogsRequest{
		ContainerId: containerID,
		Tail:        int32(tail),
		Follow:      follow,
	})
	if err != nil {
		return nil, err
	}
	return &grpcStreamReader{stream: stream}, nil
}

func (w *RemoteWorker) ContainerStats(ctx context.Context, containerID string) (*worker.ContainerStats, error) {
	resp, err := w.client.ContainerStats(ctx, &pb.ContainerStatsRequest{ContainerId: containerID})
	if err != nil {
		return nil, err
	}
	return &worker.ContainerStats{
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

func (w *RemoteWorker) CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error) {
	resp, err := w.client.CopyFromContainer(ctx, &pb.CopyFromContainerRequest{
		ContainerId: containerID,
		Path:        path,
	})
	if err != nil {
		return nil, err
	}
	return resp.Content, nil
}

func (w *RemoteWorker) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	_, err := w.client.CopyToContainer(ctx, &pb.CopyToContainerRequest{
		ContainerId: containerID,
		Path:        path,
		Content:     content,
	})
	return err
}

func (w *RemoteWorker) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	stream, err := w.client.CopyDirFromContainer(ctx, &pb.CopyDirFromContainerRequest{
		ContainerId: containerID,
		Path:        path,
	})
	if err != nil {
		return nil, err
	}
	return &grpcStreamReader{stream: stream}, nil
}

func (w *RemoteWorker) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	stream, err := w.client.CopyTarToContainer(ctx)
	if err != nil {
		return err
	}

	buf := make([]byte, 64*1024)
	first := true
	for {
		n, readErr := content.Read(buf)
		if n > 0 {
			msg := &pb.CopyTarToContainerRequest{Data: buf[:n]}
			if first {
				msg.ContainerId = containerID
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

func (w *RemoteWorker) WatchEvents(ctx context.Context) (<-chan worker.ContainerEvent, <-chan error) {
	events := make(chan worker.ContainerEvent, 64)
	errs := make(chan error, 1)

	stream, err := w.client.WatchEvents(ctx, &pb.WatchEventsRequest{})
	if err != nil {
		errs <- err
		close(events)
		close(errs)
		return events, errs
	}

	go func() {
		defer close(events)
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
			case events <- worker.ContainerEvent{
				ContainerID:   msg.ContainerId,
				ContainerName: msg.ContainerName,
				Action:        msg.Action,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return events, errs
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

func (w *RemoteWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string) (string, error) {
	resp, err := w.client.EnsureDepot(ctx, &pb.EnsureDepotRequest{
		AppId:        appID,
		Branch:       branch,
		AccountName:  accountName,
		RefreshToken: refreshToken,
	})
	if err != nil {
		return "", err
	}
	return resp.DepotDir, nil
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

func (w *RemoteWorker) ListGameserverContainers(ctx context.Context) ([]worker.GameserverContainer, error) {
	resp, err := w.client.ListGameserverContainers(ctx, &pb.ListGameserverContainersRequest{})
	if err != nil {
		return nil, fmt.Errorf("listing gameserver containers on %s: %w", w.nodeID, err)
	}
	var result []worker.GameserverContainer
	for _, c := range resp.Containers {
		result = append(result, worker.GameserverContainer{
			ContainerID:   c.ContainerId,
			ContainerName: c.ContainerName,
			GameserverID:  c.GameserverId,
			State:         c.State,
		})
	}
	return result, nil
}
