package process

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const box64Version = "v0.3.2"

// needsBox64 returns true if the host is ARM and the image contains x86 binaries.
func needsBox64(rootFS string) bool {
	if runtime.GOARCH != "arm64" {
		return false
	}

	// Check if the image's linker is x86
	x86Linkers := []string{
		filepath.Join(rootFS, "lib64", "ld-linux-x86-64.so.2"),
		filepath.Join(rootFS, "lib", "x86_64-linux-gnu", "ld-linux-x86-64.so.2"),
	}
	for _, l := range x86Linkers {
		if _, err := os.Stat(l); err == nil {
			return true
		}
	}
	return false
}

// ensureBox64 returns the path to the box64 binary, downloading it if needed.
func ensureBox64(ctx context.Context, dataDir string, log *slog.Logger) (string, error) {
	// Check if box64 is already in PATH
	if path, err := findInPath("box64"); err == nil {
		log.Debug("found box64 in PATH", "path", path)
		return path, nil
	}

	// Check if we already downloaded it
	box64Dir := filepath.Join(dataDir, "bin")
	box64Path := filepath.Join(box64Dir, "box64")
	if _, err := os.Stat(box64Path); err == nil {
		return box64Path, nil
	}

	// Download from GitHub releases
	log.Info("downloading box64", "version", box64Version)

	url := fmt.Sprintf(
		"https://github.com/ptitSeb/box64/releases/download/%s/box64-linux-arm64",
		box64Version,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating box64 download request: %w", err)
	}

	client := &http.Client{Transport: ociTransport()}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading box64: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading box64: HTTP %d", resp.StatusCode)
	}

	if err := os.MkdirAll(box64Dir, 0755); err != nil {
		return "", fmt.Errorf("creating bin dir: %w", err)
	}

	f, err := os.OpenFile(box64Path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return "", fmt.Errorf("creating box64 binary: %w", err)
	}

	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(box64Path)
		return "", fmt.Errorf("writing box64 binary: %w", err)
	}
	f.Close()

	log.Info("box64 downloaded", "path", box64Path)
	return box64Path, nil
}

// box64Env returns environment variables needed for Box64 to use the rootfs libraries.
func box64Env(rootFS string) []string {
	libPaths := []string{
		filepath.Join(rootFS, "usr", "local", "lib"),
		filepath.Join(rootFS, "usr", "lib"),
		filepath.Join(rootFS, "usr", "lib", "x86_64-linux-gnu"),
		filepath.Join(rootFS, "lib"),
		filepath.Join(rootFS, "lib", "x86_64-linux-gnu"),
		filepath.Join(rootFS, "lib64"),
	}

	binPaths := []string{
		filepath.Join(rootFS, "usr", "local", "bin"),
		filepath.Join(rootFS, "usr", "bin"),
		filepath.Join(rootFS, "bin"),
		filepath.Join(rootFS, "usr", "local", "sbin"),
		filepath.Join(rootFS, "usr", "sbin"),
		filepath.Join(rootFS, "sbin"),
	}

	return []string{
		"BOX64_LD_LIBRARY_PATH=" + strings.Join(libPaths, ":"),
		"BOX64_PATH=" + strings.Join(binPaths, ":"),
		"BOX64_LOG=0",
	}
}

func findInPath(name string) (string, error) {
	pathVar := os.Getenv("PATH")
	for _, dir := range strings.Split(pathVar, ":") {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s not found in PATH", name)
}
