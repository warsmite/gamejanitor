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
	holder    *exec.Cmd
	slirp     *exec.Cmd
	holderPID int
	slirpPID  int
	apiSock   string
	nsPID     int
}

// setupNetworkNamespace creates a user+network namespace, starts slirp4netns,
// and returns the namespace PID for nsenter.
func setupNetworkNamespace(instanceID string, ports []worker.PortBinding, dataDir string, paths *systemPaths, log *slog.Logger) (*slirpInstance, error) {
	var holderArgs []string
	if paths.IsRoot {
		holderArgs = []string{"--net", "--fork", "--",
			paths.Sh, "-c", "echo ready; exec " + paths.Sleep + " infinity"}
	} else {
		holderArgs = []string{"--user", "--net", "--fork", "--",
			paths.Sh, "-c", "echo ready; exec " + paths.Sleep + " infinity"}
	}

	holder := exec.Command(paths.Unshare, holderArgs...)
	holderOut, err := holder.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("creating holder pipe: %w", err)
	}
	holder.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
		uidStart, uidCount := SubUIDRange()
		gidStart, gidCount := SubGIDRange()
		exec.Command(paths.NewUIDMap, fmt.Sprintf("%d", nsPID),
			"0", fmt.Sprintf("%d", uid), "1",
			"1", fmt.Sprintf("%d", uidStart), fmt.Sprintf("%d", uidCount)).Run()
		exec.Command(paths.NewGIDMap, fmt.Sprintf("%d", nsPID),
			"0", fmt.Sprintf("%d", gid), "1",
			"1", fmt.Sprintf("%d", gidStart), fmt.Sprintf("%d", gidCount)).Run()
	}

	// Start slirp4netns — socket goes in /tmp to stay under the 108-char
	// Unix socket path limit (dataDir-based paths can exceed it).
	apiSock := filepath.Join(os.TempDir(), fmt.Sprintf("gj-%s.sock", instanceID))
	slirpCmd := exec.Command(paths.Slirp4netns,
		"--configure", "--mtu=65520", "--disable-host-loopback",
		"--api-socket", apiSock, fmt.Sprintf("%d", nsPID), "tap0")
	// Don't inherit stderr — slirp4netns survives parent death for restart
	// survival, and inherited fds would block the parent's I/O cleanup.
	slirpCmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
			if err := addPortForward(apiSock, p.HostPort, p.HostPort, proto); err != nil {
				log.Error("failed to add port forward", "instance", instanceID, "port", p.HostPort, "proto", proto, "error", err)
				stopSlirp(&slirpInstance{holder: holder, slirp: slirpCmd, holderPID: holder.Process.Pid, slirpPID: slirpCmd.Process.Pid, apiSock: apiSock, nsPID: nsPID}, log)
				return nil, fmt.Errorf("adding port forward %d/%s: %w", p.HostPort, proto, err)
			}
		}
	}

	si := &slirpInstance{
		holder:    holder,
		slirp:     slirpCmd,
		holderPID: holder.Process.Pid,
		slirpPID:  slirpCmd.Process.Pid,
		apiSock:   apiSock,
		nsPID:     nsPID,
	}
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
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshaling slirp request: %w", err)
	}
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("writing to slirp API: %w", err)
	}

	buf := make([]byte, 4096)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		return fmt.Errorf("reading slirp API response: %w", err)
	}
	if n > 0 {
		var resp map[string]any
		if err := json.Unmarshal(buf[:n], &resp); err != nil {
			return fmt.Errorf("parsing slirp API response: %w", err)
		}
		if errMsg, ok := resp["error"]; ok {
			return fmt.Errorf("slirp port forward: %v", errMsg)
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
	} else if si.slirpPID > 0 {
		syscall.Kill(si.slirpPID, syscall.SIGKILL)
	}
	if si.holder != nil && si.holder.Process != nil {
		si.holder.Process.Kill()
		si.holder.Wait()
	} else if si.holderPID > 0 {
		syscall.Kill(si.holderPID, syscall.SIGKILL)
	}
	os.Remove(si.apiSock)
}
