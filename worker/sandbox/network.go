package sandbox

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/warsmite/gamejanitor/worker"
)

// slirpInstance manages a slirp4netns process for a single game server.
type slirpInstance struct {
	cmd     *exec.Cmd
	apiSock string
	pid     int // PID of the child process inside the netns
}

// setupNetworkNamespace creates a network namespace and starts slirp4netns
// for the given instance. Returns the slirp instance for cleanup.
//
// The caller must have already started the game process with --unshare-net.
// slirp4netns attaches to the process's network namespace and provides:
// - Outbound connectivity (game can reach the internet)
// - Port forwarding from host ports to the namespace
func setupNetworkNamespace(instanceID string, pid int, ports []worker.PortBinding, dataDir string, slirpPath string, log *slog.Logger) (*slirpInstance, error) {
	if slirpPath == "" {
		return nil, fmt.Errorf("slirp4netns binary not available")
	}

	apiSock := filepath.Join(dataDir, "instances", instanceID, "slirp.sock")

	// Start slirp4netns attached to the game process's network namespace
	args := []string{
		"--configure",
		"--mtu=65520",
		"--disable-host-loopback", // game cannot connect to host localhost
		"--api-socket", apiSock,
		fmt.Sprintf("%d", pid),
		"tap0",
	}

	cmd := exec.Command(slirpPath, args...)
	cmd.Stdout = os.Stderr // slirp logs to stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting slirp4netns: %w", err)
	}

	si := &slirpInstance{
		cmd:     cmd,
		apiSock: apiSock,
		pid:     pid,
	}

	// Wait for API socket to appear
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(apiSock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Add port forwards for each allocated port
	for _, p := range ports {
		if p.HostPort > 0 {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			if err := addPortForward(apiSock, p.HostPort, p.HostPort, proto); err != nil {
				log.Warn("failed to add port forward", "host_port", p.HostPort, "proto", proto, "error", err)
			} else {
				log.Debug("port forward added", "host_port", p.HostPort, "proto", proto)
			}
		}
	}

	log.Info("network namespace ready", "instance", instanceID, "pid", pid, "ports", len(ports))
	return si, nil
}

// addPortForward tells slirp4netns to forward a host port into the namespace.
func addPortForward(apiSock string, hostPort, guestPort int, proto string) error {
	conn, err := net.Dial("unix", apiSock)
	if err != nil {
		return fmt.Errorf("connecting to slirp API: %w", err)
	}
	defer conn.Close()

	req := map[string]any{
		"execute": "add_hostfwd",
		"arguments": map[string]any{
			"proto":      proto,
			"host_addr":  "0.0.0.0",
			"host_port":  hostPort,
			"guest_addr": "10.0.2.100",
			"guest_port": guestPort,
		},
	}

	data, _ := json.Marshal(req)
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("sending port forward request: %w", err)
	}

	// Read response
	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("reading port forward response: %w", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return fmt.Errorf("parsing port forward response: %w", err)
	}

	if errMsg, ok := resp["error"]; ok {
		return fmt.Errorf("slirp port forward error: %v", errMsg)
	}

	return nil
}

// stopSlirp kills the slirp4netns process and cleans up.
func stopSlirp(si *slirpInstance, log *slog.Logger) {
	if si == nil || si.cmd == nil || si.cmd.Process == nil {
		return
	}
	si.cmd.Process.Kill()
	si.cmd.Wait()
	os.Remove(si.apiSock)
	log.Debug("slirp4netns stopped", "pid", si.pid)
}
