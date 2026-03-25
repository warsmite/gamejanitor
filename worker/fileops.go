package worker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/warsmite/gamejanitor/constants"
)

// volumeResolver maps a volume name to its host filesystem path.
// Docker-based workers resolve via VolumeMountpoint; process workers use plain directories.
type volumeResolver func(ctx context.Context, volumeName string) (string, error)

func resolveVolumePath(resolve volumeResolver, ctx context.Context, volumeName string, relPath string) (string, error) {
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

func listFilesDirect(resolve volumeResolver, ctx context.Context, volumeName string, path string) ([]FileEntry, error) {
	hostPath, err := resolveVolumePath(resolve, ctx, volumeName, path)
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

	sortFileEntries(entries)
	return entries, nil
}

func readFileDirect(resolve volumeResolver, ctx context.Context, volumeName string, path string) ([]byte, error) {
	hostPath, err := resolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(hostPath)
}

func writeFileDirect(resolve volumeResolver, ctx context.Context, volumeName string, path string, content []byte, perm os.FileMode) error {
	hostPath, err := resolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(hostPath, content, perm); err != nil {
		return err
	}
	return os.Chown(hostPath, constants.GameserverUID, constants.GameserverGID)
}

func deletePathDirect(resolve volumeResolver, ctx context.Context, volumeName string, path string) error {
	hostPath, err := resolveVolumePath(resolve, ctx, volumeName, path)
	if err != nil {
		return err
	}
	return os.RemoveAll(hostPath)
}

func createDirectoryDirect(resolve volumeResolver, ctx context.Context, volumeName string, path string) error {
	hostPath, err := resolveVolumePath(resolve, ctx, volumeName, path)
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

func renamePathDirect(resolve volumeResolver, ctx context.Context, volumeName string, from string, to string) error {
	fromPath, err := resolveVolumePath(resolve, ctx, volumeName, from)
	if err != nil {
		return err
	}
	toPath, err := resolveVolumePath(resolve, ctx, volumeName, to)
	if err != nil {
		return err
	}
	return os.Rename(fromPath, toPath)
}

func backupVolumeDirect(resolve volumeResolver, ctx context.Context, volumeName string) (io.ReadCloser, error) {
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

func restoreVolumeDirect(resolve volumeResolver, ctx context.Context, volumeName string, tarStream io.Reader) error {
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

func volumeSizeDirect(resolve volumeResolver, ctx context.Context, volumeName string) (int64, error) {
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

func sortFileEntries(entries []FileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
}
