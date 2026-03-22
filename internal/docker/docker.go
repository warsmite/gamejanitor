package docker

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

const (
	ContainerPrefix        = "gamejanitor-"
	UpdateContainerPrefix  = ContainerPrefix + "update-"
	FileopsContainerPrefix = ContainerPrefix + "fileops-"
)

type Client struct {
	cli *client.Client
	log *slog.Logger
}

type ContainerOptions struct {
	Name          string
	Image         string
	Env           []string // "KEY=VALUE" format
	Ports         []PortBinding
	VolumeName    string
	MemoryLimitMB int
	CPULimit      float64
	CPUEnforced   bool
	Entrypoint    []string // Override image entrypoint
	User          string   // Run as specific user (e.g., "1001:1001")
	Binds         []string // Host bind mounts in "host:container:opts" format
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

type PortBinding struct {
	HostPort      int
	ContainerPort int
	Protocol      string // "tcp" or "udp"
}

type ContainerInfo struct {
	ID        string
	State     string // "running", "exited", etc.
	StartedAt time.Time
	ExitCode  int
}

type ContainerStats struct {
	MemoryUsageMB int
	MemoryLimitMB int
	CPUPercent    float64
}

type ContainerEvent struct {
	ContainerID   string
	ContainerName string
	Action        string // "start", "stop", "die", "kill", etc.
}

func New(logger *slog.Logger) (*Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ping, err := cli.Ping(ctx)
	if err != nil {
		cli.Close()
		return nil, fmt.Errorf("pinging docker daemon: %w", err)
	}

	logger.Info("connected to docker", "api_version", ping.APIVersion)

	return &Client{cli: cli, log: logger}, nil
}

func (c *Client) Close() error {
	return c.cli.Close()
}

// PullImage pulls a Docker image. If the pull fails but the image exists locally, logs a warning and returns nil.
func (c *Client) PullImage(ctx context.Context, imageName string) error {
	c.log.Info("pulling image", "image", imageName)

	reader, err := c.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		// Check if image exists locally
		_, _, inspectErr := c.cli.ImageInspectWithRaw(ctx, imageName)
		if inspectErr == nil {
			c.log.Warn("image pull failed, using cached image", "image", imageName, "error", err)
			return nil
		}
		return fmt.Errorf("pulling image %s: %w", imageName, err)
	}
	defer reader.Close()

	// Consume the pull output to completion
	decoder := json.NewDecoder(reader)
	for {
		var msg map[string]any
		if err := decoder.Decode(&msg); err != nil {
			if err != io.EOF {
				c.log.Warn("error decoding image pull progress", "image", imageName, "error", err)
			}
			break
		}
		if status, ok := msg["status"].(string); ok {
			c.log.Debug("image pull progress", "image", imageName, "status", status)
		}
	}

	c.log.Info("image pulled", "image", imageName)
	return nil
}

func (c *Client) CreateVolume(ctx context.Context, name string) error {
	c.log.Debug("creating volume", "name", name)

	_, err := c.cli.VolumeCreate(ctx, volume.CreateOptions{
		Name: name,
	})
	if err != nil {
		return fmt.Errorf("creating volume %s: %w", name, err)
	}
	return nil
}

func (c *Client) RemoveVolume(ctx context.Context, name string) error {
	c.log.Debug("removing volume", "name", name)

	if err := c.cli.VolumeRemove(ctx, name, true); err != nil {
		return fmt.Errorf("removing volume %s: %w", name, err)
	}
	return nil
}

func (c *Client) VolumeMountpoint(ctx context.Context, name string) (string, error) {
	vol, err := c.cli.VolumeInspect(ctx, name)
	if err != nil {
		return "", fmt.Errorf("inspecting volume %s: %w", name, err)
	}
	return vol.Mountpoint, nil
}

func (c *Client) CreateContainer(ctx context.Context, opts ContainerOptions) (string, error) {
	c.log.Info("creating container", "name", opts.Name, "image", opts.Image)

	exposedPorts := nat.PortSet{}
	portBindings := nat.PortMap{}

	for _, p := range opts.Ports {
		containerPort := nat.Port(fmt.Sprintf("%d/%s", p.ContainerPort, p.Protocol))
		exposedPorts[containerPort] = struct{}{}
		portBindings[containerPort] = []nat.PortBinding{
			{HostPort: fmt.Sprintf("%d", p.HostPort)},
		}
	}

	resources := container.Resources{}
	if opts.MemoryLimitMB > 0 {
		resources.Memory = int64(opts.MemoryLimitMB) * 1024 * 1024
	}
	if opts.CPULimit > 0 && opts.CPUEnforced {
		resources.NanoCPUs = int64(opts.CPULimit * 1e9)
	}

	cfg := &container.Config{
		Image:        opts.Image,
		Env:          opts.Env,
		ExposedPorts: exposedPorts,
		User:         opts.User,
	}
	if len(opts.Entrypoint) > 0 {
		cfg.Entrypoint = opts.Entrypoint
	}

	resp, err := c.cli.ContainerCreate(ctx,
		cfg,
		&container.HostConfig{
			Binds:         opts.Binds,
			PortBindings:  portBindings,
			Resources:     resources,
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Mounts: []mount.Mount{
				{
					Type:   mount.TypeVolume,
					Source: opts.VolumeName,
					Target: "/data",
				},
			},
		},
		nil, nil, opts.Name,
	)
	if err != nil {
		return "", fmt.Errorf("creating container %s: %w", opts.Name, err)
	}

	c.log.Info("container created", "name", opts.Name, "container_id", resp.ID[:12])
	return resp.ID, nil
}

func (c *Client) StartContainer(ctx context.Context, containerID string) error {
	c.log.Debug("starting container", "container_id", shortID(containerID))

	if err := c.cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("starting container %s: %w", shortID(containerID), err)
	}
	return nil
}

func (c *Client) StopContainer(ctx context.Context, containerID string, timeoutSeconds int) error {
	c.log.Debug("stopping container", "container_id", shortID(containerID), "timeout", timeoutSeconds)

	timeout := timeoutSeconds
	if err := c.cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout}); err != nil {
		return fmt.Errorf("stopping container %s: %w", shortID(containerID), err)
	}
	return nil
}

func (c *Client) RemoveContainer(ctx context.Context, containerID string) error {
	c.log.Debug("removing container", "container_id", shortID(containerID))

	if err := c.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("removing container %s: %w", shortID(containerID), err)
	}
	return nil
}

func (c *Client) InspectContainer(ctx context.Context, containerID string) (*ContainerInfo, error) {
	resp, err := c.cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspecting container %s: %w", shortID(containerID), err)
	}

	startedAt, err := time.Parse(time.RFC3339Nano, resp.State.StartedAt)
	if err != nil {
		c.log.Warn("failed to parse container started_at", "container_id", shortID(containerID), "raw", resp.State.StartedAt, "error", err)
	}

	return &ContainerInfo{
		ID:        resp.ID,
		State:     resp.State.Status,
		StartedAt: startedAt,
		ExitCode:  resp.State.ExitCode,
	}, nil
}

// Exec runs a command inside a container and returns the output.
func (c *Client) Exec(ctx context.Context, containerID string, cmd []string) (int, string, string, error) {
	c.log.Debug("exec in container", "container_id", shortID(containerID), "cmd", cmd)

	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := c.cli.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return -1, "", "", fmt.Errorf("creating exec in %s: %w", shortID(containerID), err)
	}

	attachResp, err := c.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, "", "", fmt.Errorf("attaching exec in %s: %w", shortID(containerID), err)
	}
	defer attachResp.Close()

	// Docker multiplexes stdout/stderr in the attach stream using an 8-byte header per frame:
	// [stream_type(1)][0(3)][size(4)][payload(size)]
	// stream_type: 1=stdout, 2=stderr
	var stdout, stderr strings.Builder
	header := make([]byte, 8)
	for {
		_, err := io.ReadFull(attachResp.Reader, header)
		if err != nil {
			break
		}
		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}
		payload := make([]byte, frameSize)
		_, err = io.ReadFull(attachResp.Reader, payload)
		if err != nil {
			break
		}
		switch streamType {
		case 1:
			stdout.Write(payload)
		case 2:
			stderr.Write(payload)
		}
	}

	inspectResp, err := c.cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return -1, stdout.String(), stderr.String(), fmt.Errorf("inspecting exec in %s: %w", shortID(containerID), err)
	}

	return inspectResp.ExitCode, stdout.String(), stderr.String(), nil
}

// ContainerLogs returns a log stream from the container.
func (c *Client) ContainerLogs(ctx context.Context, containerID string, tail int, follow bool) (io.ReadCloser, error) {
	c.log.Debug("reading container logs", "container_id", shortID(containerID), "tail", tail, "follow", follow)

	tailStr := "all"
	if tail > 0 {
		tailStr = fmt.Sprintf("%d", tail)
	}

	reader, err := c.cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     follow,
		Tail:       tailStr,
	})
	if err != nil {
		return nil, fmt.Errorf("reading logs for %s: %w", shortID(containerID), err)
	}

	return reader, nil
}

// ParseLogLines reads Docker's multiplexed log stream and returns all lines.
// Stderr lines are prefixed with "[ERR] ".
func ParseLogLines(r io.Reader) []string {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)
	var lines []string

	for {
		if _, err := io.ReadFull(br, header); err != nil {
			break
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			break
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines = append(lines, prefix+line)
			}
		}
	}

	return lines
}

// ParseLogStream reads Docker's multiplexed log stream and sends lines to the channel.
// Stderr lines are prefixed with "[ERR] ". Closes when the stream ends.
func ParseLogStream(r io.Reader, lines chan<- string) {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)

	for {
		if _, err := io.ReadFull(br, header); err != nil {
			return
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines <- prefix + line
			}
		}
	}
}

// ContainerStats returns current resource usage for a container.
func (c *Client) ContainerStats(ctx context.Context, containerID string) (*ContainerStats, error) {
	// stream=false (not one-shot) waits to collect two samples for accurate CPU delta
	resp, err := c.cli.ContainerStats(ctx, containerID, false)
	if err != nil {
		return nil, fmt.Errorf("getting stats for %s: %w", shortID(containerID), err)
	}
	defer resp.Body.Close()

	var stats container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, fmt.Errorf("decoding stats for %s: %w", shortID(containerID), err)
	}

	memUsageMB := int(stats.MemoryStats.Usage / (1024 * 1024))
	memLimitMB := int(stats.MemoryStats.Limit / (1024 * 1024))

	// CPU percent calculation
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	cpuPercent := 0.0
	if systemDelta > 0 && cpuDelta > 0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(stats.CPUStats.OnlineCPUs) * 100.0
	}

	return &ContainerStats{
		MemoryUsageMB: memUsageMB,
		MemoryLimitMB: memLimitMB,
		CPUPercent:    cpuPercent,
	}, nil
}

// CopyFromContainer reads a single file from the container and returns its contents.
func (c *Client) CopyFromContainer(ctx context.Context, containerID string, path string) ([]byte, error) {
	c.log.Debug("copying from container", "container_id", shortID(containerID), "path", path)

	reader, _, err := c.cli.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, fmt.Errorf("copying from %s:%s: %w", shortID(containerID), path, err)
	}
	defer reader.Close()

	tr := tar.NewReader(reader)
	hdr, err := tr.Next()
	if err != nil {
		return nil, fmt.Errorf("reading tar header from %s:%s: %w", shortID(containerID), path, err)
	}
	if hdr.Typeflag == tar.TypeDir {
		return nil, fmt.Errorf("%s is a directory", path)
	}

	content, err := io.ReadAll(tr)
	if err != nil {
		return nil, fmt.Errorf("reading file content from %s:%s: %w", shortID(containerID), path, err)
	}
	return content, nil
}

// CopyToContainer writes a single file into the container at the given path.
func (c *Client) CopyToContainer(ctx context.Context, containerID string, path string, content []byte) error {
	c.log.Debug("copying to container", "container_id", shortID(containerID), "path", path)

	dir := filepath.Dir(path)
	filename := filepath.Base(path)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Files must be owned by gameserver (1001:1001) — Docker's CopyToContainer
	// extracts as root, so without explicit UID/GID files end up root-owned
	// and game scripts running as gameserver can't modify them.
	if err := tw.WriteHeader(&tar.Header{
		Name: filename,
		Mode: 0644,
		Size: int64(len(content)),
		Uid:  1001,
		Gid:  1001,
	}); err != nil {
		return fmt.Errorf("writing tar header for %s: %w", path, err)
	}
	if _, err := tw.Write(content); err != nil {
		return fmt.Errorf("writing tar content for %s: %w", path, err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar writer for %s: %w", path, err)
	}

	if err := c.cli.CopyToContainer(ctx, containerID, dir, &buf, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copying to %s:%s: %w", shortID(containerID), path, err)
	}
	return nil
}

// CopyDirFromContainer returns a tar stream of a directory from the container.
// The caller is responsible for closing the returned ReadCloser.
func (c *Client) CopyDirFromContainer(ctx context.Context, containerID string, path string) (io.ReadCloser, error) {
	c.log.Debug("copying directory from container", "container_id", shortID(containerID), "path", path)

	reader, _, err := c.cli.CopyFromContainer(ctx, containerID, path)
	if err != nil {
		return nil, fmt.Errorf("copying dir from %s:%s: %w", shortID(containerID), path, err)
	}
	return reader, nil
}

// CopyTarToContainer extracts a tar stream into a directory in the container.
func (c *Client) CopyTarToContainer(ctx context.Context, containerID string, destPath string, content io.Reader) error {
	c.log.Debug("copying tar to container", "container_id", shortID(containerID), "path", destPath)

	if err := c.cli.CopyToContainer(ctx, containerID, destPath, content, container.CopyToContainerOptions{}); err != nil {
		return fmt.Errorf("copying tar to %s:%s: %w", shortID(containerID), destPath, err)
	}
	return nil
}

// WatchEvents subscribes to Docker events for gamejanitor containers.
func (c *Client) WatchEvents(ctx context.Context) (<-chan ContainerEvent, <-chan error) {
	c.log.Info("starting docker event watcher")

	eventCh := make(chan ContainerEvent)
	errCh := make(chan error, 1)

	msgCh, msgErrCh := c.cli.Events(ctx, events.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("type", string(events.ContainerEventType)),
			filters.Arg("event", "start"),
			filters.Arg("event", "stop"),
			filters.Arg("event", "die"),
			filters.Arg("event", "kill"),
		),
	})

	go func() {
		defer close(eventCh)
		defer close(errCh)

		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-msgErrCh:
				if !ok {
					return
				}
				errCh <- err
				return
			case msg, ok := <-msgCh:
				if !ok {
					return
				}
				name := msg.Actor.Attributes["name"]
				if !strings.HasPrefix(name, ContainerPrefix) {
					continue
				}

				event := ContainerEvent{
					ContainerID:   msg.Actor.ID,
					ContainerName: name,
					Action:        string(msg.Action),
				}
				c.log.Debug("docker event", "container", name, "action", event.Action)

				select {
				case eventCh <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return eventCh, errCh
}
