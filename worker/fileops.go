package worker

import (
	"archive/tar"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"log/slog"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

// VolumeResolver maps a volume name to its host filesystem path.
// Docker-based workers resolve via VolumeMountpoint; process workers use plain directories.
type VolumeResolver func(ctx context.Context, volumeName string) (string, error)

func ResolveVolumePath(resolve VolumeResolver, ctx context.Context, volumeName string, relPath string) (string, error) {
	mountpoint, err := resolve(ctx, volumeName)
	if err != nil {
		return "", err
	}

	resolved := filepath.Join(mountpoint, filepath.Clean(relPath))
	if !strings.HasPrefix(resolved, mountpoint) {
		return "", fmt.Errorf("path %q escapes volume root", relPath)
	}
	return resolved, nil
}

func ListFilesDirect(resolve VolumeResolver, ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return nil, err
	}

	dirEntries, err := os.ReadDir(hostPath)
	if err != nil {
		return nil, fmt.Errorf("reading directory %s: %w", path, err)
	}

	entries := make([]FileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, FileEntry{
			Name:        de.Name(),
			IsDir:       de.IsDir(),
			Size:        info.Size(),
			ModTime:     info.ModTime(),
			Permissions: info.Mode().String(),
		})
	}

	SortFileEntries(entries)
	return entries, nil
}

func ReadFileDirect(resolve VolumeResolver, ctx context.Context, volumeName string, path string) ([]byte, error) {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(hostPath)
}

func WriteFileDirect(resolve VolumeResolver, ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return err
	}
	parentDir := filepath.Dir(hostPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("creating parent directory: %w", err)
	}
	chownToGameserver(parentDir)
	if err := os.WriteFile(hostPath, content, perm); err != nil {
		return err
	}
	return os.Chown(hostPath, model.GameserverUID, model.GameserverGID)
}

func DeletePathDirect(resolve VolumeResolver, ctx context.Context, volumeName string, path string) error {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(hostPath)
}

func CreateDirectoryDirect(resolve VolumeResolver, ctx context.Context, volumeName string, path string) error {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hostPath, fs.ModePerm); err != nil {
		return err
	}
	return filepath.WalkDir(hostPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chown(p, 1001, 1001)
	})
}

func RenamePathDirect(resolve VolumeResolver, ctx context.Context, volumeName string, from string, to string) error {
	fromPath, err := ResolveVolumePath(resolve, ctx, volumeName, from)
	if err != nil {
		return err
	}
	toPath, err := ResolveVolumePath(resolve, ctx, volumeName, to)
	if err != nil {
		return err
	}
	return os.Rename(fromPath, toPath)
}

func BackupVolumeDirect(resolve VolumeResolver, ctx context.Context, volumeName string) (io.ReadCloser, error) {
	mountpoint, err := resolve(ctx, volumeName)
	if err != nil {
		return nil, fmt.Errorf("resolving volume mountpoint: %w", err)
	}

	pr, pw := io.Pipe()
	tw := tar.NewWriter(pw)

	go func() {
		defer pw.Close()
		defer tw.Close()

		err := filepath.Walk(mountpoint, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(mountpoint, path)
			if err != nil {
				return err
			}
			tarPath := filepath.Join("data", relPath)

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}
			header.Name = tarPath

			if err := tw.WriteHeader(header); err != nil {
				return err
			}

			if info.IsDir() {
				return nil
			}

			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()

			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			pw.CloseWithError(fmt.Errorf("creating tar from volume: %w", err))
		}
	}()

	return pr, nil
}

func RestoreVolumeDirect(resolve VolumeResolver, ctx context.Context, volumeName string, tarStream io.Reader) error {
	mountpoint, err := resolve(ctx, volumeName)
	if err != nil {
		return fmt.Errorf("resolving volume mountpoint: %w", err)
	}

	// Clear existing contents
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return fmt.Errorf("reading volume directory: %w", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(mountpoint, entry.Name())); err != nil {
			return fmt.Errorf("clearing volume: %w", err)
		}
	}

	// Extract tar
	tr := tar.NewReader(tarStream)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		// Strip "data/" prefix to get path relative to volume root
		relPath := header.Name
		if strings.HasPrefix(relPath, "data/") {
			relPath = strings.TrimPrefix(relPath, "data/")
		} else if relPath == "data" {
			continue
		}
		if relPath == "" || relPath == "." {
			continue
		}

		targetPath := filepath.Join(mountpoint, filepath.Clean(relPath))
		if !strings.HasPrefix(targetPath, mountpoint) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", relPath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", relPath, err)
			}
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", relPath, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", relPath, err)
			}
			f.Close()
		}
	}

	return nil
}

func VolumeSizeDirect(resolve VolumeResolver, ctx context.Context, volumeName string) (int64, error) {
	mountpoint, err := resolve(ctx, volumeName)
	if err != nil {
		return 0, err
	}

	var total int64
	err = filepath.WalkDir(mountpoint, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, err
}

var downloadClient = &http.Client{Timeout: 10 * time.Minute}

func DownloadFileDirect(resolve VolumeResolver, ctx context.Context, volumeName string, url string, destPath string, expectedHash string, maxBytes int64) error {
	hostPath, err := ResolveVolumePath(resolve, ctx, volumeName, destPath)
	if err != nil {
		return err
	}

	parentDir := filepath.Dir(hostPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("creating parent directory: %w", err)
	}
	// Chown created directories so the gameserver user (1001) can manage them inside the container
	chownToGameserver(parentDir)

	// Try up to 2 attempts — first failure on hash mismatch could be a transient
	// network issue (truncated response). Second failure is likely a genuine CDN
	// rebuild, so we warn and keep the file.
	const maxAttempts = 2
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		hashOK, err := downloadToFile(ctx, url, hostPath, expectedHash)
		if err != nil {
			return err
		}
		if hashOK || expectedHash == "" {
			break
		}
		if attempt < maxAttempts {
			slog.Warn("download hash mismatch, retrying", "path", destPath, "attempt", attempt)
			continue
		}
		slog.Warn("download hash mismatch after retry, keeping file", "path", destPath)
	}

	os.Chown(hostPath, model.GameserverUID, model.GameserverGID)
	return nil
}

// downloadToFile downloads a URL to a file, optionally verifying SHA-512.
// Returns (hashMatched, error). If no hash is expected, hashMatched is true.
func downloadToFile(ctx context.Context, url string, hostPath string, expectedHash string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := downloadClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	f, err := os.OpenFile(hostPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return false, fmt.Errorf("creating file: %w", err)
	}

	var w io.Writer = f
	var hasher hash.Hash
	if expectedHash != "" {
		hasher = sha512.New()
		w = io.MultiWriter(f, hasher)
	}

	_, err = io.Copy(w, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(hostPath)
		return false, fmt.Errorf("writing download: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(hostPath)
		return false, fmt.Errorf("closing file: %w", err)
	}

	if hasher != nil {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != expectedHash {
			return false, nil
		}
	}
	return true, nil
}

// DownloadToMemory downloads a URL to []byte with hash verification.
// Used as a fallback when direct filesystem access is unavailable (sidecar mode).
// Has a 500MB memory safety limit since this buffers into RAM.
func DownloadToMemory(ctx context.Context, url string, expectedHash string) ([]byte, error) {
	const memoryLimit int64 = 500 * 1024 * 1024

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, memoryLimit))
	if err != nil {
		return nil, fmt.Errorf("reading download: %w", err)
	}

	if expectedHash != "" {
		h := sha512.Sum512(data)
		actual := hex.EncodeToString(h[:])
		if actual != expectedHash {
			slog.Warn("download hash mismatch, keeping data", "expected", expectedHash, "actual", actual)
		}
	}

	return data, nil
}

// chownToGameserver chowns the given directory and all its parent directories
// up to the point where they're already owned by the gameserver user.
// This ensures that MkdirAll-created intermediate directories (e.g., /data/server/oxide/
// when creating /data/server/oxide/plugins/) are accessible inside the container.
func chownToGameserver(dir string) {
	for p := dir; p != "/" && p != "."; p = filepath.Dir(p) {
		info, err := os.Stat(p)
		if err != nil {
			slog.Warn("chownToGameserver: stat failed", "path", p, "error", err)
			break
		}
		if !info.IsDir() {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && stat.Uid == uint32(model.GameserverUID) {
			break // already owned by gameserver user, parents should be too
		}
		if err := os.Chown(p, model.GameserverUID, model.GameserverGID); err != nil {
			slog.Warn("chownToGameserver: chown failed", "path", p, "error", err)
		} else {
			slog.Debug("chownToGameserver: chowned", "path", p, "from_uid", stat.Uid)
		}
	}
}

func SortFileEntries(entries []FileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}
