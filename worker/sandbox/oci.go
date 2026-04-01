package sandbox

import (
	"archive/tar"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// pullAndExtractOCIImage pulls an instance image and extracts its filesystem to destDir.
// Skips extraction if the image has already been extracted (digest marker file present).
// Returns the parsed image config (entrypoint, cmd, env, working dir).
func pullAndExtractOCIImage(ctx context.Context, imageName string, destDir string, log *slog.Logger) (*imageConfig, error) {
	ref, err := name.ParseReference(imageName)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference %s: %w", imageName, err)
	}

	log.Info("pulling OCI image", "image", imageName)

	img, err := remote.Image(ref, remote.WithContext(ctx), remote.WithTransport(ociTransport()))
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
		// Ensure index is up to date (may be missing from older versions)
		updateImageIndex(destDir, imageName, digest.Algorithm+"/"+digest.Hex)
		cfgData, _ := json.Marshal(cfg)
		os.WriteFile(filepath.Join(extractDir, ".config.json"), cfgData, 0644)
		return cfg, nil
	}

	os.RemoveAll(extractDir)
	if err := os.MkdirAll(extractDir, 0755); err != nil {
		return nil, fmt.Errorf("creating extraction dir: %w", err)
	}

	layers, err := img.Layers()
	if err != nil {
		return nil, fmt.Errorf("getting image layers: %w", err)
	}

	layersDir := filepath.Join(destDir, "layers")
	os.MkdirAll(layersDir, 0755)

	log.Info("downloading image layers", "image", imageName, "count", len(layers))

	// Download and cache layers by digest (parallel)
	layerDirs := make([]string, len(layers))
	errCh := make(chan error, len(layers))

	for i, layer := range layers {
		i, layer := i, layer
		go func() {
			layerDigest, err := layer.Digest()
			if err != nil {
				errCh <- fmt.Errorf("layer %d digest: %w", i, err)
				return
			}

			layerDir := filepath.Join(layersDir, layerDigest.Hex)
			layerMarker := filepath.Join(layerDir, ".extracted")

			if _, err := os.Stat(layerMarker); err == nil {
				// Layer already cached
				layerDirs[i] = layerDir
				errCh <- nil
				return
			}

			os.RemoveAll(layerDir)
			os.MkdirAll(layerDir, 0755)

			rc, err := layer.Uncompressed()
			if err != nil {
				errCh <- fmt.Errorf("decompressing layer %d: %w", i, err)
				return
			}

			if err := extractTarLayer(rc, layerDir); err != nil {
				rc.Close()
				errCh <- fmt.Errorf("extracting layer %d: %w", i, err)
				return
			}
			rc.Close()

			os.WriteFile(layerMarker, nil, 0644)
			layerDirs[i] = layerDir
			errCh <- nil
		}()
	}

	for range layers {
		if err := <-errCh; err != nil {
			return nil, err
		}
	}

	log.Info("merging layers", "image", imageName, "layers", len(layers))

	// Try overlayfs mount for zero-copy layer merging
	if err := tryOverlayMount(layerDirs, extractDir); err != nil {
		// Fallback: merge layers sequentially into flat directory
		log.Warn("overlayfs unavailable — using flat extraction (slower first start, more disk usage). Requires kernel 5.11+ for optimal performance.", "reason", err)
		for i, layerDir := range layerDirs {
			if err := mergeLayerDir(layerDir, extractDir); err != nil {
				return nil, fmt.Errorf("merging layer %d: %w", i, err)
			}
		}
	}

	if err := os.WriteFile(markerFile, []byte(imageName), 0644); err != nil {
		return nil, fmt.Errorf("writing extraction marker: %w", err)
	}

	// Save image config to disk so imageRootFS doesn't need network access
	cfgData, _ := json.Marshal(cfg)
	os.WriteFile(filepath.Join(extractDir, ".config.json"), cfgData, 0644)

	// Update image index so imageRootFS can find it without network access
	if err := updateImageIndex(destDir, imageName, digest.Algorithm+"/"+digest.Hex); err != nil {
		return nil, fmt.Errorf("updating image index: %w", err)
	}

	log.Info("image extracted", "image", imageName, "digest", digest.Hex[:12])
	return cfg, nil
}

func updateImageIndex(imagesDir string, imageName string, relDir string) error {
	indexPath := filepath.Join(imagesDir, "index.json")

	var index map[string]string
	if data, err := os.ReadFile(indexPath); err == nil {
		json.Unmarshal(data, &index)
	}
	if index == nil {
		index = make(map[string]string)
	}

	index[imageName] = relDir

	data, err := json.Marshal(index)
	if err != nil {
		return err
	}
	return os.WriteFile(indexPath, data, 0644)
}

// ociTransport returns an HTTP transport with a fallback DNS resolver.
// Uses the system resolver by default. Falls back to public DNS (8.8.8.8)
// when the system resolver fails — needed on Android/Termux where
// /etc/resolv.conf is empty.
func ociTransport() http.RoundTripper {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Resolver: &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				// Try system resolver first
				conn, err := d.DialContext(ctx, network, address)
				if err == nil {
					return conn, nil
				}
				// Fall back to public DNS (Android/Termux, broken resolv.conf)
				return d.DialContext(ctx, "udp", "8.8.8.8:53")
			},
		},
	}
	tlsConfig := &tls.Config{}

	// On Android/Termux, CA certs are in non-standard locations
	certPaths := []string{
		"/etc/ssl/certs/ca-certificates.crt",
		"/etc/tls/cacert.pem",
		"/data/data/com.termux/files/usr/etc/tls/cert.pem",
		"/system/etc/security/cacerts",
	}
	for _, p := range certPaths {
		if _, err := os.Stat(p); err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if data, err := os.ReadFile(p); err == nil {
				pool.AppendCertsFromPEM(data)
			}
			tlsConfig.RootCAs = pool
			break
		}
	}

	return &http.Transport{
		DialContext:         dialer.DialContext,
		TLSClientConfig:    tlsConfig,
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// ensureResolvConf writes a fallback resolv.conf to dataDir if the system one is missing or empty.
// Returns the path to a working resolv.conf for binding into sandboxes.
func ensureResolvConf(dataDir string) string {
	// Check if system resolv.conf works
	systemConf := "/etc/resolv.conf"
	if data, err := os.ReadFile(systemConf); err == nil {
		content := strings.TrimSpace(string(data))
		if content != "" && strings.Contains(content, "nameserver") {
			// Verify the nameserver isn't just localhost (broken on Android)
			for _, line := range strings.Split(content, "\n") {
				line = strings.TrimSpace(line)
				if strings.HasPrefix(line, "nameserver") {
					addr := strings.TrimSpace(strings.TrimPrefix(line, "nameserver"))
					if addr != "::1" && addr != "127.0.0.1" {
						return systemConf
					}
				}
			}
		}
	}

	// Write a fallback resolv.conf
	fallback := filepath.Join(dataDir, "resolv.conf")
	os.WriteFile(fallback, []byte("nameserver 8.8.8.8\nnameserver 1.1.1.1\n"), 0644)
	return fallback
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
			if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
				return fmt.Errorf("creating parent dir for link %s: %w", header.Name, err)
			}
			if err := os.Link(linkTarget, targetPath); err != nil {
				// Hard links can fail on some filesystems or due to permissions — fall back to copy
				if copyErr := copyFile(linkTarget, targetPath); copyErr != nil {
					return fmt.Errorf("creating hard link %s -> %s: %w (copy fallback also failed: %v)", header.Name, header.Linkname, err, copyErr)
				}
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
// Looks up the image by reading the index file written during extraction (no network needed).
func imageRootFS(imagesDir string, imageName string) (string, *imageConfig, error) {
	// Read the index file that maps image names to digest directories
	indexPath := filepath.Join(imagesDir, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return "", nil, fmt.Errorf("image index not found (run PullImage first): %w", err)
	}

	var index map[string]string // image name -> digest dir (relative to imagesDir)
	if err := json.Unmarshal(indexData, &index); err != nil {
		return "", nil, fmt.Errorf("parsing image index: %w", err)
	}

	relDir, ok := index[imageName]
	if !ok {
		return "", nil, fmt.Errorf("image %s not found in index (run PullImage first)", imageName)
	}

	rootFS := filepath.Join(imagesDir, relDir)
	markerFile := filepath.Join(rootFS, ".extracted")
	if _, err := os.Stat(markerFile); err != nil {
		return "", nil, fmt.Errorf("image %s not extracted (run PullImage first)", imageName)
	}

	// Read cached config from disk
	var cfg imageConfig
	cfgData, err := os.ReadFile(filepath.Join(rootFS, ".config.json"))
	if err != nil {
		return "", nil, fmt.Errorf("reading cached image config: %w", err)
	}
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		return "", nil, fmt.Errorf("parsing cached image config: %w", err)
	}

	return rootFS, &cfg, nil
}
