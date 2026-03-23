package worker

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// imageConfig holds the parsed entrypoint/cmd/env from an OCI image.
type imageConfig struct {
	Entrypoint []string
	Cmd        []string
	Env        []string
	WorkingDir string
}

// pullAndExtractOCIImage pulls a container image and extracts its filesystem to destDir.
// Skips extraction if the image has already been extracted (digest marker file present).
// Returns the parsed image config (entrypoint, cmd, env, working dir).
func pullAndExtractOCIImage(ctx context.Context, imageName string, destDir string, log *slog.Logger) (*imageConfig, error) {
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference %s: %w", imageName, err)
	}

	log.Info("pulling OCI image", "image", imageName)

	img, err := remote.Image(ref, remote.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("pulling image %s: %w", imageName, err)
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, fmt.Errorf("getting image digest: %w", err)
	}

	extractDir := filepath.Join(destDir, digest.Algorithm, digest.Hex)
	markerFile := filepath.Join(extractDir, ".extracted")

	cfg, err := extractImageConfig(img)
	if err != nil {
		return nil, err
	}

	if _, err := os.Stat(markerFile); err == nil {
		log.Info("image already extracted, skipping", "image", imageName, "digest", digest.Hex[:12])
		return cfg, nil
	}

	os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return nil, fmt.Errorf("creating extraction dir: %w", err)
	}

	log.Info("extracting image layers", "image", imageName, "dest", extractDir)

	// Extract layers individually instead of using mutate.Extract,
	// which flattens layers and can lose symlinks when a later layer
	// re-emits a directory entry for a path that should be a symlink.
	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("getting image layers: %w", err)
	}

	for i, layer := range layers {
		rc, err := layer.Uncompressed()
		if err != nil {
			return nil, fmt.Errorf("decompressing layer %d: %w", i, err)
		}

		if err := extractTarLayer(rc, extractDir); err != nil {
			rc.Close()
			return nil, fmt.Errorf("extracting layer %d: %w", i, err)
		}
		rc.Close()
	}

	if err := os.WriteFile(markerFile, []byte(imageName), 0644); err != nil {
		return nil, fmt.Errorf("writing extraction marker: %w", err)
	}

	log.Info("image extracted", "image", imageName, "digest", digest.Hex[:12])
	return cfg, nil
}

func extractTarLayer(r io.Reader, extractDir string) error {
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		name := filepath.Clean(header.Name)

		// Handle OCI whiteout files — these mark deletions from previous layers
		base := filepath.Base(name)
		if strings.HasPrefix(base, ".wh.") {
			deleteName := strings.TrimPrefix(base, ".wh.")
			if deleteName == ".wh..opq" {
				// Opaque whiteout: clear entire directory
				dir := filepath.Join(extractDir, filepath.Dir(name))
				entries, _ := os.ReadDir(dir)
				for _, e := range entries {
					os.RemoveAll(filepath.Join(dir, e.Name()))
				}
			} else {
				// Single file whiteout
				target := filepath.Join(extractDir, filepath.Dir(name), deleteName)
				os.RemoveAll(target)
			}
			continue
		}

		targetPath := filepath.Join(extractDir, name)
		if !filepath.HasPrefix(targetPath, extractDir) {
			continue
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetPath, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating directory %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			os.Remove(targetPath)
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("creating parent dir for %s: %w", header.Name, err)
			}
			f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return fmt.Errorf("creating file %s: %w", header.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing file %s: %w", header.Name, err)
			}
			f.Close()
		case tar.TypeSymlink:
			os.Remove(targetPath)
			if err := os.Symlink(header.Linkname, targetPath); err != nil {
				return fmt.Errorf("creating symlink %s -> %s: %w", header.Name, header.Linkname, err)
			}
		case tar.TypeLink:
			linkTarget := filepath.Join(extractDir, filepath.Clean(header.Linkname))
			os.Remove(targetPath)
			if err := os.Link(linkTarget, targetPath); err != nil {
				return fmt.Errorf("creating hard link %s -> %s: %w", header.Name, header.Linkname, err)
			}
		}
	}
	return nil
}

func extractImageConfig(img v1.Image) (*imageConfig, error) {
	cfgFile, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("reading image config: %w", err)
	}

	return &imageConfig{
		Entrypoint: cfgFile.Config.Entrypoint,
		Cmd:        cfgFile.Config.Cmd,
		Env:        cfgFile.Config.Env,
		WorkingDir: cfgFile.Config.WorkingDir,
	}, nil
}

// imageRootFS returns the filesystem path for an already-extracted image.
func imageRootFS(imagesDir string, imageName string) (string, *imageConfig, error) {
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return "", nil, fmt.Errorf("parsing image reference: %w", err)
	}

	desc, err := remote.Image(ref)
	if err != nil {
		return "", nil, fmt.Errorf("fetching image descriptor: %w", err)
	}

	digest, err := desc.Digest()
	if err != nil {
		return "", nil, fmt.Errorf("getting image digest: %w", err)
	}

	rootFS := filepath.Join(imagesDir, digest.Algorithm, digest.Hex)
	markerFile := filepath.Join(rootFS, ".extracted")
	if _, err := os.Stat(markerFile); err != nil {
		return "", nil, fmt.Errorf("image %s not extracted (run PullImage first)", imageName)
	}

	cfg, err := extractImageConfig(desc)
	if err != nil {
		return "", nil, err
	}

	return rootFS, cfg, nil
}
