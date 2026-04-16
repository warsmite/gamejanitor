package local

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// tryOverlayMount attempts to mount layers as an overlay filesystem.
// lowerDirs are ordered bottom-to-top (first = lowest layer).
// mergedDir is where the unified view appears.
// Returns an error if overlayfs is not available (kernel <5.11, no permissions).
func tryOverlayMount(lowerDirs []string, mergedDir string) error {
	if len(lowerDirs) == 0 {
		return fmt.Errorf("no layers to mount")
	}

	// overlayfs lowerdir is colon-separated, highest priority first
	reversed := make([]string, len(lowerDirs))
	for i, d := range lowerDirs {
		reversed[len(lowerDirs)-1-i] = d
	}
	lowerOpt := strings.Join(reversed, ":")

	// Create workdir and upperdir for overlayfs
	parent := filepath.Dir(mergedDir)
	upperDir := filepath.Join(parent, ".overlay-upper")
	workDir := filepath.Join(parent, ".overlay-work")
	os.MkdirAll(upperDir, 0755)
	os.MkdirAll(workDir, 0755)
	os.MkdirAll(mergedDir, 0755)

	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", lowerOpt, upperDir, workDir)

	err := syscall.Mount("overlay", mergedDir, "overlay", 0, opts)
	if err != nil {
		// Clean up on failure
		os.RemoveAll(upperDir)
		os.RemoveAll(workDir)
		return fmt.Errorf("overlay mount: %w", err)
	}

	return nil
}

// mergeLayerDir copies the contents of a layer directory into the target,
// handling overwrites. Used as a fallback when overlayfs is unavailable.
func mergeLayerDir(layerDir, targetDir string) error {
	return filepath.WalkDir(layerDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(layerDir, path)
		if err != nil {
			return err
		}

		// Skip marker files
		if rel == ".extracted" {
			return nil
		}

		target := filepath.Join(targetDir, rel)

		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}

		// Handle symlinks
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			os.Remove(target)
			return os.Symlink(linkTarget, target)
		}

		// Regular file — copy
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		defer src.Close()

		os.MkdirAll(filepath.Dir(target), 0755)
		os.Remove(target)
		dst, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
		if err != nil {
			return err
		}
		defer dst.Close()

		_, err = copyIO(dst, src)
		return err
	})
}

func copyIO(dst *os.File, src *os.File) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			nw, werr := dst.Write(buf[:n])
			total += int64(nw)
			if werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if err == io.EOF {
				return total, nil
			}
			return total, err
		}
	}
}
