package mod

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

// Size limits for in-memory reads (ZIP entry extraction, .mrpack manifests).
// File downloads to disk have no artificial limit — storage quota is the real constraint.
const (
	maxOverrideBytes     = 50 * 1024 * 1024  // 50 MB per override file
	maxPackManifestBytes = 500 * 1024 * 1024  // 500 MB for .mrpack ZIP (manifest + overrides, not mod files)
)

// --- FileDelivery ---

// FileDelivery tells the worker to download a file from a URL directly to the gameserver volume.
type FileDelivery struct {
	fileSvc FileOperator
	log     *slog.Logger
}

func NewFileDelivery(fileSvc FileOperator, log *slog.Logger) *FileDelivery {
	return &FileDelivery{
		fileSvc: fileSvc,
		log:     log,
	}
}

func (d *FileDelivery) Install(ctx context.Context, gameserverID, installPath, downloadURL, fileName string) error {
	if err := d.fileSvc.CreateDirectory(ctx, gameserverID, installPath); err != nil {
		return fmt.Errorf("creating install directory %s: %w", installPath, err)
	}

	fullPath := path.Join(installPath, fileName)
	if err := d.fileSvc.DownloadToVolume(ctx, gameserverID, downloadURL, fullPath, "", 0); err != nil {
		return fmt.Errorf("downloading mod %s: %w", fileName, err)
	}

	return nil
}

func (d *FileDelivery) Uninstall(ctx context.Context, gameserverID, filePath string) error {
	if filePath == "" {
		return nil
	}
	if err := d.fileSvc.DeletePath(ctx, gameserverID, filePath); err != nil {
		d.log.Warn("failed to delete mod file, continuing anyway", "path", filePath, "error", err)
	}
	return nil
}

// --- ManifestDelivery ---

// ManifestDelivery writes mod IDs to a JSON manifest file.
// The game server reads this manifest and downloads mods itself via SteamCMD.
type ManifestDelivery struct {
	fileSvc FileOperator
	log     *slog.Logger
}

func NewManifestDelivery(fileSvc FileOperator, log *slog.Logger) *ManifestDelivery {
	return &ManifestDelivery{fileSvc: fileSvc, log: log}
}

func (d *ManifestDelivery) Install(ctx context.Context, gameserverID, manifestPath string, allModIDs []string) error {
	dir := path.Dir(manifestPath)
	if err := d.fileSvc.CreateDirectory(ctx, gameserverID, dir); err != nil {
		return fmt.Errorf("creating manifest directory %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(allModIDs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling manifest: %w", err)
	}

	if err := d.fileSvc.WriteFile(ctx, gameserverID, manifestPath, data); err != nil {
		return fmt.Errorf("writing manifest: %w", err)
	}
	return nil
}

func (d *ManifestDelivery) Uninstall(ctx context.Context, gameserverID, manifestPath string, remainingModIDs []string) error {
	if len(remainingModIDs) == 0 {
		if err := d.fileSvc.DeletePath(ctx, gameserverID, manifestPath); err != nil {
			d.log.Warn("failed to delete empty manifest", "path", manifestPath, "error", err)
		}
		return nil
	}
	return d.Install(ctx, gameserverID, manifestPath, remainingModIDs)
}

// --- PackDelivery ---

// PackDelivery installs a modpack (.mrpack) — a bundle of mods + config overrides.
type PackDelivery struct {
	fileSvc FileOperator
	client  *http.Client
	log     *slog.Logger
}

func NewPackDelivery(fileSvc FileOperator, log *slog.Logger) *PackDelivery {
	return &PackDelivery{
		fileSvc: fileSvc,
		client:  &http.Client{Timeout: 5 * time.Minute},
		log:     log,
	}
}

// PackContents is the parsed result of installing a modpack.
type PackContents struct {
	Mods      []PackMod
	Overrides []string // paths of config files extracted
}

type PackMod struct {
	SourceID    string
	FileName    string
	FilePath    string
	DownloadURL string
	SHA512      string
}

func (d *PackDelivery) Install(ctx context.Context, gameserverID, packURL, installPath, overridesPath string) (*PackContents, error) {
	// Download the .mrpack file
	packData, err := d.download(ctx, packURL)
	if err != nil {
		return nil, fmt.Errorf("downloading modpack: %w", err)
	}

	// Parse the ZIP
	zipReader, err := zip.NewReader(bytes.NewReader(packData), int64(len(packData)))
	if err != nil {
		return nil, fmt.Errorf("opening modpack ZIP: %w", err)
	}

	// Read modrinth.index.json
	index, err := d.readIndex(zipReader)
	if err != nil {
		return nil, fmt.Errorf("reading modpack index: %w", err)
	}

	// Ensure install directory exists
	if err := d.fileSvc.CreateDirectory(ctx, gameserverID, installPath); err != nil {
		return nil, fmt.Errorf("creating mod install directory: %w", err)
	}

	// Download each mod file directly to the worker volume
	var mods []PackMod
	for _, f := range index.Files {
		// Filter: skip client-only mods
		if f.Env != nil && f.Env.Server == "unsupported" {
			continue
		}

		if len(f.Downloads) == 0 {
			d.log.Warn("modpack file has no download URLs, skipping", "path", f.Path)
			continue
		}

		downloadURL := f.Downloads[0]
		fileName := path.Base(f.Path)
		fullPath := path.Join(installPath, fileName)
		expectedHash := f.Hashes["sha512"]

		if err := d.fileSvc.DownloadToVolume(ctx, gameserverID, downloadURL, fullPath, expectedHash, 0); err != nil {
			return nil, fmt.Errorf("downloading pack mod %s: %w", fileName, err)
		}

		mods = append(mods, PackMod{
			FileName:    fileName,
			FilePath:    fullPath,
			DownloadURL: downloadURL,
			SHA512:      expectedHash,
		})
	}

	// Extract server-overrides
	var overrides []string
	for _, zf := range zipReader.File {
		if !strings.HasPrefix(zf.Name, "server-overrides/") {
			continue
		}
		relPath := strings.TrimPrefix(zf.Name, "server-overrides/")
		if relPath == "" || strings.HasSuffix(relPath, "/") {
			continue
		}

		// Path traversal protection
		if strings.Contains(relPath, "..") || path.IsAbs(relPath) {
			d.log.Warn("skipping suspicious override path", "path", relPath)
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("opening override %s: %w", relPath, err)
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxOverrideBytes))
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("reading override %s: %w", relPath, err)
		}

		fullPath := path.Join(overridesPath, relPath)
		dir := path.Dir(fullPath)
		if err := d.fileSvc.CreateDirectory(ctx, gameserverID, dir); err != nil {
			d.log.Warn("failed to create override directory", "dir", dir, "error", err)
			continue
		}
		if err := d.fileSvc.WriteFile(ctx, gameserverID, fullPath, content); err != nil {
			return nil, fmt.Errorf("writing override %s: %w", relPath, err)
		}
		overrides = append(overrides, fullPath)
	}

	// Also check plain "overrides/" folder (applied before server-overrides)
	for _, zf := range zipReader.File {
		if !strings.HasPrefix(zf.Name, "overrides/") || strings.HasPrefix(zf.Name, "overrides/../") {
			continue
		}
		relPath := strings.TrimPrefix(zf.Name, "overrides/")
		if relPath == "" || strings.HasSuffix(relPath, "/") {
			continue
		}
		if strings.Contains(relPath, "..") || path.IsAbs(relPath) {
			continue
		}

		rc, err := zf.Open()
		if err != nil {
			continue
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxOverrideBytes))
		rc.Close()
		if err != nil {
			continue
		}

		fullPath := path.Join(overridesPath, relPath)
		dir := path.Dir(fullPath)
		d.fileSvc.CreateDirectory(ctx, gameserverID, dir)
		d.fileSvc.WriteFile(ctx, gameserverID, fullPath, content)
		overrides = append(overrides, fullPath)
	}

	return &PackContents{Mods: mods, Overrides: overrides}, nil
}

// mrpack index types

type mrpackIndex struct {
	FormatVersion int          `json:"formatVersion"`
	Game          string       `json:"game"`
	VersionID     string       `json:"versionId"`
	Name          string       `json:"name"`
	Files         []mrpackFile `json:"files"`
	Dependencies  map[string]string `json:"dependencies"`
}

type mrpackFile struct {
	Path      string            `json:"path"`
	Hashes    map[string]string `json:"hashes"`
	Env       *mrpackEnv        `json:"env,omitempty"`
	Downloads []string          `json:"downloads"`
	FileSize  int64             `json:"fileSize"`
}

type mrpackEnv struct {
	Client string `json:"client"`
	Server string `json:"server"`
}

func (d *PackDelivery) readIndex(zr *zip.Reader) (*mrpackIndex, error) {
	for _, f := range zr.File {
		if f.Name == "modrinth.index.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()

			var index mrpackIndex
			if err := json.NewDecoder(rc).Decode(&index); err != nil {
				return nil, fmt.Errorf("decoding modrinth.index.json: %w", err)
			}
			return &index, nil
		}
	}
	return nil, fmt.Errorf("modrinth.index.json not found in modpack")
}

func (d *PackDelivery) download(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxPackManifestBytes))
}

