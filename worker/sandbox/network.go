package sandbox

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/worker"
)

// slirpInstance manages the network namespace holder and slirp4netns process.
type slirpInstance struct {
	holder  *exec.Cmd // unshare process holding the netns open
	slirp   *exec.Cmd
	apiSock string
	nsPID   int // PID of the namespace holder (for nsenter)
}

// setupNetworkNamespace creates a user+network namespace, starts slirp4netns
// for connectivity, and returns the namespace PID for nsenter.
func setupNetworkNamespace(instanceID string, ports []worker.PortBinding, dataDir string, slirpPath string, log *slog.Logger) (*slirpInstance, error) {
	if slirpPath == "" {
		return nil, fmt.Errorf("slirp4netns binary not available")
	}

	// Step 1: Create a process that holds the user+network namespace open
	holder := exec.Command("unshare", "--user", "--net", "--fork", "--kill-child", "--", "sh", "-c", "echo ready; exec sleep infinity")
	holderOut, err := holder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating holder pipe: %w", err)
	}
	holder.Stderr = os.Stderr
	holder.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	if err := holder.Start(); err != nil {
		return nil, fmt.Errorf("starting namespace holder: %w", err)
	}

	// Wait for "ready" signal
	scanner := bufio.NewScanner(holderOut)
	if !scanner.Scan() {
		holder.Process.Kill()
		holder.Wait()
		return nil, fmt.Errorf("namespace holder did not signal ready")
	}

	nsPID := holder.Process.Pid

	// Map a full UID/GID range so chown inside the namespace works.
	// Maps current user to root (0), plus a subordinate range for UIDs 1-65536.
	uid := os.Getuid()
	gid := os.Getgid()
	if err := exec.Command("newuidmap", fmt.Sprintf("%d", nsPID),
		"0", fmt.Sprintf("%d", uid), "1",
		"1", "165536", "65536").Run(); err != nil {
		log.Warn("newuidmap failed, chown inside sandbox may not work", "error", err)
	}
	if err := exec.Command("newgidmap", fmt.Sprintf("%d", nsPID),
		"0", fmt.Sprintf("%d", gid), "1",
		"1", "165536", "65536").Run(); err != nil {
		log.Warn("newgidmap failed, chown inside sandbox may not work", "error", err)
	}

	log.Debug("network namespace created", "holder_pid", nsPID)

	// Step 2: Start slirp4netns attached to the namespace holder
	apiSock := filepath.Join(dataDir, "instances", instanceID, "slirp.sock")
	slirpArgs := []string{
		"--configure",
		"--mtu=65520",
		"--disable-host-loopback",
		"--api-socket", apiSock,
		fmt.Sprintf("%d", nsPID),
		"tap0",
	}

	slirpCmd := exec.Command(slirpPath, slirpArgs...)
	slirpCmd.Stderr = os.Stderr
	slirpCmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}

	if err := slirpCmd.Start(); err != nil {
		holder.Process.Kill()
		holder.Wait()
		return nil, fmt.Errorf("starting slirp4netns: %w", err)
	}

	// Wait for API socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(apiSock); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Step 3: Add port forwards
	for _, p := range ports {
		if p.HostPort > 0 {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			if err := addPortForward(apiSock, p.HostPort, p.HostPort, proto); err != nil {
				log.Warn("failed to add port forward", "host_port", p.HostPort, "proto", proto, "error", err)
			}
		}
	}

	si := &slirpInstance{
		holder:  holder,
		slirp:   slirpCmd,
		apiSock: apiSock,
		nsPID:   nsPID,
	}

	log.Info("network namespace ready", "instance", instanceID, "ns_pid", nsPID, "ports", len(ports))
	return si, nil
}

// nsenterPrefix returns the nsenter command prefix to run a process inside
// the network namespace. Used to wrap bwrap so it inherits the netns.
func nsenterPrefix(nsPID int) []string {
	return []string{
		"nsenter",
		fmt.Sprintf("--user=/proc/%d/ns/user", nsPID),
		fmt.Sprintf("--net=/proc/%d/ns/net", nsPID),
		"--",
	}
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
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("sending port forward request: %w", err)
	}

	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("reading port forward response: %w", err)
	}

	var resp map[string]any
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		return fmt.Errorf("parsing response: %w", err)
	}
	if errMsg, ok := resp["error"]; ok {
		return fmt.Errorf("slirp error: %v", errMsg)
	}

	return nil
}

// stopSlirp kills the slirp4netns process and namespace holder.
func stopSlirp(si *slirpInstance, log *slog.Logger) {
	if si == nil {
		return
	}
	if si.slirp != nil && si.slirp.Process != nil {
		si.slirp.Process.Kill()
		si.slirp.Wait()
	}
	if si.holder != nil && si.holder.Process != nil {
		si.holder.Process.Kill()
		si.holder.Wait()
	}
	os.Remove(si.apiSock)
	log.Debug("network namespace cleaned up", "ns_pid", si.nsPID)
}
