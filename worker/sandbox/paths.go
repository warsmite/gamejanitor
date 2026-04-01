package sandbox

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
)

// systemPaths holds resolved paths to system binaries used by the sandbox.
// All paths are resolved once at startup to avoid repeated lookups and
// PATH issues inside systemd units.
type systemPaths struct {
	Bwrap       string
	Slirp4netns string
	Unshare     string
	Nsenter     string
	Sh          string
	Sleep       string
	Rm          string
	Systemctl   string
	NewUIDMap   string // empty if not available
	NewGIDMap   string // empty if not available
	IsRoot      bool
}

// resolvePaths finds all required system binaries. Returns an error if
// critical binaries (bwrap) are missing.
func resolvePaths(dataDir string, log *slog.Logger) (*systemPaths, error) {
	p := &systemPaths{
		IsRoot: os.Getuid() == 0,
	}

	var err error

	// bwrap and slirp from embedded or system
	p.Bwrap, err = ensureBwrap(dataDir, log)
	if err != nil {
		return nil, fmt.Errorf("bwrap not available: %w", err)
	}

	p.Slirp4netns, err = ensureSlirp4netns(dataDir, log)
	if err != nil {
		log.Warn("slirp4netns not available, network isolation disabled", "error", err)
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

	// Validate critical paths
	if p.Unshare == "" {
		log.Warn("unshare not found, network isolation disabled")
	}
	if p.Systemctl == "" {
		log.Warn("systemctl not found, resource limits and process survival unavailable")
	}

	return p, nil
}

// hasSystemd returns true if systemd-run is usable.
func (p *systemPaths) hasSystemd() bool {
	return p.Systemctl != ""
}

// hasNetworkIsolation returns true if all binaries for network namespace are available.
func (p *systemPaths) hasNetworkIsolation() bool {
	return p.Slirp4netns != "" && p.Unshare != "" && p.Sh != "" && p.Sleep != ""
}

// hasUIDMapping returns true if newuidmap/newgidmap are available (non-root only).
func (p *systemPaths) hasUIDMapping() bool {
	return p.NewUIDMap != "" && p.NewGIDMap != ""
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
