package runtime

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// NetNS represents a persistent network namespace backed by a holder process.
// The holder is a re-exec of the current binary that sleeps in a new
// user+network namespace. The namespace lives as long as the holder is alive.
type NetNS struct {
	Path     string // /proc/<holder-pid>/ns/net
	UserPath string // /proc/<holder-pid>/ns/user
	cmd      *exec.Cmd
}

// CreateNetNS creates a new network namespace by forking a holder process
// in a new user+network namespace. The namespace path is /proc/<pid>/ns/net.
// Works for both root and rootless — CLONE_NEWUSER is always used.
func CreateNetNS() (*NetNS, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding self executable: %w", err)
	}

	uid := os.Getuid()
	gid := os.Getgid()

	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "_GAMEJANITOR_NETNS_HOLD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: uid, Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: gid, Size: 1}},
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("forking netns holder: %w", err)
	}

	pid := cmd.Process.Pid
	return &NetNS{
		Path:     fmt.Sprintf("/proc/%d/ns/net", pid),
		UserPath: fmt.Sprintf("/proc/%d/ns/user", pid),
		cmd:      cmd,
	}, nil
}

// Close kills the holder process, destroying the network namespace.
func (ns *NetNS) Close() {
	if ns == nil || ns.cmd == nil || ns.cmd.Process == nil {
		return
	}
	ns.cmd.Process.Kill()
	ns.cmd.Wait()
}

// MaybeHandleNetNSChild handles the forked child case. Call early in main().
// Returns true if this process is a netns helper child.
func MaybeHandleNetNSChild() bool {
	if os.Getenv("_GAMEJANITOR_NETNS_HOLD") == "" {
		return false
	}
	select {}
}
