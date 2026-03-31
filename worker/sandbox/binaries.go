package sandbox

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	bwrapVersion      = "0.10.0"
	slirp4netnsVersion = "1.3.1"
)

// bwrapURL returns the download URL for a static bwrap binary.
func bwrapURL() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}
	return fmt.Sprintf("https://github.com/containers/bubblewrap/releases/download/v%s/bwrap-%s", bwrapVersion, arch)
}

// slirp4netnsURL returns the download URL for a static slirp4netns binary.
func slirp4netnsURL() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}
	return fmt.Sprintf("https://github.com/rootless-containers/slirp4netns/releases/download/v%s/slirp4netns-%s", slirp4netnsVersion, arch)
}

// ensureBwrap returns the path to the bwrap binary. Checks system PATH first,
// then the bundled location, downloading if needed.
func ensureBwrap(dataDir string, log *slog.Logger) (string, error) {
	// Check system PATH first
	if path, err := exec.LookPath("bwrap"); err == nil {
		return path, nil
	}

	return ensureBinary(dataDir, "bwrap", bwrapURL(), log)
}

// ensureSlirp4netns returns the path to the slirp4netns binary.
func ensureSlirp4netns(dataDir string, log *slog.Logger) (string, error) {
	if path, err := exec.LookPath("slirp4netns"); err == nil {
		return path, nil
	}

	return ensureBinary(dataDir, "slirp4netns", slirp4netnsURL(), log)
}

// ensureBinary downloads a binary to {dataDir}/bin/{name} if it doesn't exist.
func ensureBinary(dataDir, name, url string, log *slog.Logger) (string, error) {
	binDir := filepath.Join(dataDir, "bin")
	binPath := filepath.Join(binDir, name)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	log.Info("downloading sandbox binary", "name", name, "url", url)

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("creating bin directory: %w", err)
	}

	resp, err := http.Get(url)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", name, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: HTTP %d", name, resp.StatusCode)
	}

	tmpPath := binPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("creating temp file for %s: %w", name, err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("writing %s: %w", name, err)
	}
	f.Close()

	if err := os.Rename(tmpPath, binPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("installing %s: %w", name, err)
	}

	log.Info("sandbox binary installed", "name", name, "path", binPath)
	return binPath, nil
}
