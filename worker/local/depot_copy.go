package local

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// CopyDepotToVolume copies depot files into the volume's /server directory
// using pure Go (no external commands). This avoids both the instance's cgroup
// memory limit (OOM on large depots) and PATH issues on NixOS.
func CopyDepotToVolume(depotDir string, volumeMountpoint string) error {
	serverDir := filepath.Join(volumeMountpoint, "server")
	if err := os.MkdirAll(serverDir, 0755); err != nil {
		return fmt.Errorf("creating server dir: %w", err)
	}

	err := filepath.Walk(depotDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(depotDir, path)
		if err != nil {
			return err
		}
		targetPath := filepath.Join(serverDir, relPath)

		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}

		return copyFile(path, targetPath, info.Mode())
	})
	if err != nil {
		return fmt.Errorf("copying depot to volume: %w", err)
	}

	// Files stay owned by the caller (root). The runtime handles UID mapping:
	// The runtime handles UID mapping.

	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
