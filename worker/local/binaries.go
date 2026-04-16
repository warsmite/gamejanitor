package local

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/warsmite/gamejanitor/worker/local/embedded"
)

// ensureBwrap returns the path to the bwrap binary.
// Checks system PATH first, then extracts the embedded static binary.
func ensureBwrap(dataDir string, log *slog.Logger) (string, error) {
	if path, err := exec.LookPath("bwrap"); err == nil {
		return path, nil
	}
	return extractEmbeddedBinary(dataDir, "bwrap", log)
}

// extractEmbeddedBinary extracts a static binary from the embedded filesystem
// to {dataDir}/bin/{name}. Skips if already extracted.
func extractEmbeddedBinary(dataDir, name string, log *slog.Logger) (string, error) {
	binDir := filepath.Join(dataDir, "bin")
	binPath := filepath.Join(binDir, name)

	if _, err := os.Stat(binPath); err == nil {
		return binPath, nil
	}

	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x86_64"
	} else if arch == "arm64" {
		arch = "aarch64"
	}

	embedName := name + "-" + arch
	data, err := embedded.Binaries.ReadFile(embedName)
	if err != nil {
		return "", fmt.Errorf("embedded %s binary not found for %s: %w", name, arch, err)
	}

	if err := os.MkdirAll(binDir, 0755); err != nil {
		return "", fmt.Errorf("creating bin directory: %w", err)
	}

	if err := os.WriteFile(binPath, data, 0755); err != nil {
		return "", fmt.Errorf("extracting %s: %w", name, err)
	}

	log.Info("extracted embedded binary", "name", name, "path", binPath)
	return binPath, nil
}
