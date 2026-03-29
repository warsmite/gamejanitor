package mod

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
)

// Size limit for in-memory reads of individual override files from ZIP entries.
const maxOverrideBytes = 50 * 1024 * 1024 // 50 MB per override file

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
	fileSvc     FileOperator
	client      *http.Client
	log         *slog.Logger
	ValidateURL func(string) error
}

func NewPackDelivery(fileSvc FileOperator, log *slog.Logger) *PackDelivery {
	return &PackDelivery{
		fileSvc: fileSvc,
		// No fixed timeout — modpack downloads can be large on slow networks.
		// The caller's context handles cancellation.
		client:  &http.Client{},
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
	// Download .mrpack to a temp file to avoid buffering large ZIPs in memory
	tmpFile, err := d.downloadToTemp(ctx, packURL)
	if err != nil {
		return nil, fmt.Errorf("downloading modpack: %w", err)
	}
	defer os.Remove(tmpFile)

	// Parse the ZIP from disk
	zipReader, err := zip.OpenReader(tmpFile)
	if err != nil {
		return nil, fmt.Errorf("opening modpack ZIP: %w", err)
	}
	defer zipReader.Close()

	// Parse the modpack index (format-agnostic)
	packFiles, err := d.parsePackIndex(&zipReader.Reader)
	if err != nil {
		return nil, fmt.Errorf("reading modpack index: %w", err)
	}

	// Ensure install directory exists
	if err := d.fileSvc.CreateDirectory(ctx, gameserverID, installPath); err != nil {
		return nil, fmt.Errorf("creating mod install directory: %w", err)
	}

	// Download each mod file directly to the worker volume
	var mods []PackMod
	for _, f := range packFiles {
		// Filter: skip client-only mods
		if f.ServerSide == "unsupported" {
			continue
		}

		if d.ValidateURL != nil {
			if err := d.ValidateURL(f.DownloadURL); err != nil {
				return nil, fmt.Errorf("blocked download URL in modpack: %v", err)
			}
		}

		fileName := path.Base(f.Path)
		fullPath := path.Join(installPath, fileName)

		if err := d.fileSvc.DownloadToVolume(ctx, gameserverID, f.DownloadURL, fullPath, f.SHA512, 0); err != nil {
			return nil, fmt.Errorf("downloading pack mod %s: %w", fileName, err)
		}

		mods = append(mods, PackMod{
			SourceID:    extractModrinthProjectID(f.DownloadURL, fileName),
			FileName:    fileName,
			FilePath:    fullPath,
			DownloadURL: f.DownloadURL,
			SHA512:      f.SHA512,
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
			d.log.Warn("failed to open override file", "path", relPath, "error", err)
			continue
		}
		content, err := io.ReadAll(io.LimitReader(rc, maxOverrideBytes))
		rc.Close()
		if err != nil {
			d.log.Warn("failed to read override file", "path", relPath, "error", err)
			continue
		}

		fullPath := path.Join(overridesPath, relPath)
		dir := path.Dir(fullPath)
		if err := d.fileSvc.CreateDirectory(ctx, gameserverID, dir); err != nil {
			d.log.Warn("failed to create override directory", "dir", dir, "error", err)
			continue
		}
		if err := d.fileSvc.WriteFile(ctx, gameserverID, fullPath, content); err != nil {
			d.log.Warn("failed to write override file", "path", relPath, "error", err)
			continue
		}
		overrides = append(overrides, fullPath)
	}

	return &PackContents{Mods: mods, Overrides: overrides}, nil
}

// --- Pack format abstraction ---

// packFile is a format-agnostic representation of a file in a modpack.
// Both mrpack (Modrinth) and future formats (CurseForge, Thunderstore) produce these.
type packFile struct {
	Path         string // relative path inside the pack (e.g., "mods/lithium.jar")
	DownloadURL  string
	SHA512       string
	ServerSide   string // "required", "optional", "unsupported", or "" (unknown)
}

// parsePackIndex detects the modpack format from the ZIP contents and parses it.
// Currently supports Modrinth (.mrpack). Future formats can be added here.
func (d *PackDelivery) parsePackIndex(zr *zip.Reader) ([]packFile, error) {
	// Try Modrinth format (modrinth.index.json)
	for _, f := range zr.File {
		if f.Name == "modrinth.index.json" {
			return d.parseMrpackIndex(f)
		}
	}

	// Future: try CurseForge format (manifest.json)
	// for _, f := range zr.File {
	//     if f.Name == "manifest.json" {
	//         return d.parseCurseForgeIndex(f)
	//     }
	// }

	return nil, fmt.Errorf("unrecognized modpack format: no modrinth.index.json found")
}

// --- Modrinth .mrpack format ---

type mrpackIndex struct {
	FormatVersion int               `json:"formatVersion"`
	Game          string            `json:"game"`
	VersionID     string            `json:"versionId"`
	Name          string            `json:"name"`
	Files         []mrpackFile      `json:"files"`
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

func (d *PackDelivery) parseMrpackIndex(f *zip.File) ([]packFile, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	var index mrpackIndex
	if err := json.NewDecoder(rc).Decode(&index); err != nil {
		return nil, fmt.Errorf("decoding modrinth.index.json: %w", err)
	}

	var files []packFile
	for _, mf := range index.Files {
		if len(mf.Downloads) == 0 {
			d.log.Warn("modpack file has no download URLs, skipping", "path", mf.Path)
			continue
		}
		serverSide := ""
		if mf.Env != nil {
			serverSide = mf.Env.Server
		}
		files = append(files, packFile{
			Path:        mf.Path,
			DownloadURL: mf.Downloads[0],
			SHA512:      mf.Hashes["sha512"],
			ServerSide:  serverSide,
		})
	}
	return files, nil
}

// downloadToTemp downloads a URL to a temporary file and returns the file path.
// Caller is responsible for removing the file when done.
func (d *PackDelivery) downloadToTemp(ctx context.Context, downloadURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := d.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	tmpFile, err := os.CreateTemp("", "gamejanitor-mrpack-*.zip")
	if err != nil {
		return "", fmt.Errorf("creating temp file: %w", err)
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return "", fmt.Errorf("writing temp file: %w", err)
	}
	tmpFile.Close()
	return tmpFile.Name(), nil
}

// extractModrinthProjectID pulls the project ID from a Modrinth CDN URL.
// URL format: https://cdn.modrinth.com/data/{projectID}/versions/{versionID}/{filename}
// Falls back to the filename as a unique identifier if the URL doesn't match.
func extractModrinthProjectID(downloadURL, fallback string) string {
	const prefix = "/data/"
	idx := strings.Index(downloadURL, prefix)
	if idx == -1 {
		return fallback
	}
	rest := downloadURL[idx+len(prefix):]
	if slash := strings.Index(rest, "/"); slash > 0 {
		return rest[:slash]
	}
	return fallback
}

