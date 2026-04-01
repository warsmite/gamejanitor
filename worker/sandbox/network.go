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
	holder  *exec.Cmd
	slirp   *exec.Cmd
	apiSock string
	nsPID   int
}

// setupNetworkNamespace creates a user+network namespace, starts slirp4netns,
// and returns the namespace PID for nsenter.
func setupNetworkNamespace(instanceID string, ports []worker.PortBinding, dataDir string, paths *systemPaths, log *slog.Logger) (*slirpInstance, error) {
	var holderArgs []string
	if paths.IsRoot {
		holderArgs = []string{"--net", "--fork", "--kill-child", "--",
			paths.Sh, "-c", "echo ready; exec " + paths.Sleep + " infinity"}
	} else {
		holderArgs = []string{"--user", "--net", "--fork", "--kill-child", "--",
			paths.Sh, "-c", "echo ready; exec " + paths.Sleep + " infinity"}
	}

	holder := exec.Command(paths.Unshare, holderArgs...)
	holderOut, err := holder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating holder pipe: %w", err)
	}
	holder.Stderr = os.Stderr
	holder.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}

	if err := holder.Start(); err != nil {
		return nil, fmt.Errorf("starting namespace holder: %w", err)
	}

	scanner := bufio.NewScanner(holderOut)
	if !scanner.Scan() {
		holder.Process.Kill()
		holder.Wait()
		return nil, fmt.Errorf("namespace holder did not signal ready")
	}

	nsPID := holder.Process.Pid

	// Map UID/GID range for non-root so chown works inside the namespace
	if !paths.IsRoot && paths.hasUIDMapping() {
		uid := os.Getuid()
		gid := os.Getgid()
		exec.Command(paths.NewUIDMap, fmt.Sprintf("%d", nsPID),
			"0", fmt.Sprintf("%d", uid), "1", "1", "165536", "65536").Run()
		exec.Command(paths.NewGIDMap, fmt.Sprintf("%d", nsPID),
			"0", fmt.Sprintf("%d", gid), "1", "1", "165536", "65536").Run()
	}

	// Start slirp4netns
	apiSock := filepath.Join(dataDir, "instances", instanceID, "slirp.sock")
	slirpCmd := exec.Command(paths.Slirp4netns,
		"--configure", "--mtu=65520", "--disable-host-loopback",
		"--api-socket", apiSock, fmt.Sprintf("%d", nsPID), "tap0")
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

	// Add port forwards
	for _, p := range ports {
		if p.HostPort > 0 {
			proto := p.Protocol
			if proto == "" {
				proto = "tcp"
			}
			addPortForward(apiSock, p.HostPort, p.HostPort, proto)
		}
	}

	si := &slirpInstance{holder: holder, slirp: slirpCmd, apiSock: apiSock, nsPID: nsPID}
	log.Info("network namespace ready", "instance", instanceID, "ns_pid", nsPID, "ports", len(ports))
	return si, nil
}

// nsenterPrefix returns the nsenter command prefix for entering the namespace.
func nsenterPrefix(nsPID int, paths *systemPaths) []string {
	if paths.IsRoot {
		return []string{paths.Nsenter, fmt.Sprintf("--net=/proc/%d/ns/net", nsPID), "--"}
	}
	return []string{paths.Nsenter, "--preserve-credentials",
		fmt.Sprintf("--user=/proc/%d/ns/user", nsPID),
		fmt.Sprintf("--net=/proc/%d/ns/net", nsPID), "--"}
}

func addPortForward(apiSock string, hostPort, guestPort int, proto string) error {
	conn, err := net.Dial("unix", apiSock)
	if err != nil {
		return fmt.Errorf("connecting to slirp API: %w", err)
	}
	defer conn.Close()

	req := map[string]any{
		"execute": "add_hostfwd",
		"arguments": map[string]any{
			"proto": proto, "host_addr": "0.0.0.0", "host_port": hostPort,
			"guest_addr": "10.0.2.100", "guest_port": guestPort,
		},
	}
	data, _ := json.Marshal(req)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.Write(data)

	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := conn.Read(buf)
	if n > 0 {
		var resp map[string]any
		json.Unmarshal(buf[:n], &resp)
		if errMsg, ok := resp["error"]; ok {
			return fmt.Errorf("slirp: %v", errMsg)
		}
	}
	return nil
}

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
}
