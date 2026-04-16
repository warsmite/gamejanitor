package local

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// systemPaths holds resolved paths to system binaries used by the sandbox.
// All paths are resolved once at startup to avoid repeated lookups and
// PATH issues inside systemd units.
type systemPaths struct {
	Bwrap     string
	Unshare   string
	Nsenter   string
	Sh        string
	Sleep     string
	Rm        string
	Systemctl string
	NewUIDMap string // empty if not available
	NewGIDMap string // empty if not available
	IsRoot    bool
}

// resolvePaths finds all required system binaries. Returns an error if
// critical binaries (bwrap) are missing.
func resolvePaths(dataDir string, log *slog.Logger) (*systemPaths, error) {
	p := &systemPaths{
		IsRoot: os.Getuid() == 0,
	}

	var err error

	p.Bwrap, err = ensureBwrap(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("bwrap not available: %w", err)
	}

	// System utilities
	p.Unshare = lookupBinary("unshare")
	p.Nsenter = lookupBinary("nsenter")
	p.Sh = lookupBinary("sh")
	p.Sleep = lookupBinary("sleep")
	p.Rm = lookupBinary("rm")
	p.Systemctl = lookupBinary("systemctl")
	p.NewUIDMap = lookupBinary("newuidmap")
	p.NewGIDMap = lookupBinary("newgidmap")

	if p.Systemctl == "" {
		log.Warn("systemctl not found — no resource limits (memory/CPU), processes will not survive gamejanitor restarts")
	}
	if !p.IsRoot && !p.hasUIDMapping() {
		log.Warn("newuidmap/newgidmap not found — game install scripts that chown to UID 1001 may fail. Install 'uidmap' package or 'shadow' on your distribution.")
	}

	return p, nil
}

// hasSystemd returns true if systemd-run is usable.
func (p *systemPaths) hasSystemd() bool {
	return p.Systemctl != ""
}

// hasUIDMapping returns true if newuidmap/newgidmap are available (non-root only).
func (p *systemPaths) hasUIDMapping() bool {
	return p.NewUIDMap != "" && p.NewGIDMap != ""
}

// SubUIDRange reads the subordinate UID range for the current user from /etc/subuid.
// Returns start, count. Falls back to 165536, 65536 if unreadable.
func SubUIDRange() (int, int) {
	return readSubRange("/etc/subuid")
}

// SubGIDRange reads the subordinate GID range for the current user from /etc/subgid.
func SubGIDRange() (int, int) {
	return readSubRange("/etc/subgid")
}

func readSubRange(path string) (int, int) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 165536, 65536
	}
	username := os.Getenv("USER")
	uid := fmt.Sprintf("%d", os.Getuid())
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		if fields[0] == username || fields[0] == uid {
			start, _ := strconv.Atoi(fields[1])
			count, _ := strconv.Atoi(fields[2])
			if start > 0 && count > 0 {
				return start, count
			}
		}
	}
	return 165536, 65536
}

// lookupBinary searches PATH and common system locations for a binary.
func lookupBinary(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	for _, dir := range []string{"/usr/bin", "/bin", "/usr/sbin", "/sbin", "/run/current-system/sw/bin"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}
