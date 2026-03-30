package steam

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	goproto "google.golang.org/protobuf/proto"

	"github.com/warsmite/gamejanitor/steam/proto"
)

// DepotCache manages on-disk caching of downloaded depot files and manifests.
// Cache layout:
//
//	{root}/
//	  {app_id}/
//	    {depot_id}/
//	      manifest.bin          — raw manifest binary (for delta updates)
//	      manifest_meta.json    — depot metadata (manifest ID, build ID)
//	      files/                — the actual game files
type DepotCache struct {
	root string
	log  *slog.Logger
}

// CacheMeta stores metadata about a cached depot download.
type CacheMeta struct {
	AppID      uint32 `json:"app_id"`
	DepotID    uint32 `json:"depot_id"`
	ManifestID uint64 `json:"manifest_id"`
	BuildID    string `json:"build_id"`
	Branch     string `json:"branch"`
}

// NewDepotCache creates a depot cache at the given root directory.
func NewDepotCache(root string, log *slog.Logger) *DepotCache {
	return &DepotCache{
		root: root,
		log:  log.With("component", "steam_cache"),
	}
}

// DepotDir returns the path to a depot's cached files directory.
func (c *DepotCache) DepotDir(appID, depotID uint32) string {
	return filepath.Join(c.root, fmt.Sprintf("%d", appID), fmt.Sprintf("%d", depotID))
}

// FilesDir returns the path to the actual game files for a depot.
func (c *DepotCache) FilesDir(appID, depotID uint32) string {
	return filepath.Join(c.DepotDir(appID, depotID), "files")
}

// AppFilesDir returns a shared directory where all depots for an app are merged.
// This is the directory that gets bind-mounted into the container.
func (c *DepotCache) AppFilesDir(appID uint32) string {
	return filepath.Join(c.root, fmt.Sprintf("%d", appID), "merged")
}

// GetMeta reads the cached metadata for a depot. Returns nil if not cached.
func (c *DepotCache) GetMeta(appID, depotID uint32) *CacheMeta {
	path := filepath.Join(c.DepotDir(appID, depotID), "manifest_meta.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	meta := &CacheMeta{}
	if err := json.Unmarshal(data, meta); err != nil {
		c.log.Warn("corrupt cache metadata, will re-download",
			"app_id", appID,
			"depot_id", depotID,
			"error", err,
		)
		return nil
	}

	return meta
}

// SaveMeta writes metadata for a cached depot.
func (c *DepotCache) SaveMeta(meta *CacheMeta) error {
	dir := c.DepotDir(meta.AppID, meta.DepotID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, "manifest_meta.json"), data, 0o644)
}

// GetManifest loads the cached manifest for delta updates. Returns nil if not cached.
func (c *DepotCache) GetManifest(appID, depotID uint32, depotKey []byte) *Manifest {
	path := filepath.Join(c.DepotDir(appID, depotID), "manifest.bin")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	meta := c.GetMeta(appID, depotID)
	if meta == nil {
		return nil
	}

	manifest, err := parseManifestFromCache(data, depotID, meta.ManifestID, depotKey)
	if err != nil {
		c.log.Warn("failed to parse cached manifest, will re-download",
			"app_id", appID,
			"depot_id", depotID,
			"error", err,
		)
		return nil
	}

	return manifest
}

// SaveManifest stores the raw manifest data for future delta updates.
func (c *DepotCache) SaveManifest(appID, depotID uint32, manifestData []byte) error {
	dir := c.DepotDir(appID, depotID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.bin"), manifestData, 0o644)
}

// RemoveDepot deletes all cached data for a depot.
func (c *DepotCache) RemoveDepot(appID, depotID uint32) error {
	return os.RemoveAll(c.DepotDir(appID, depotID))
}

// RemoveApp deletes all cached data for an app.
func (c *DepotCache) RemoveApp(appID uint32) error {
	return os.RemoveAll(filepath.Join(c.root, fmt.Sprintf("%d", appID)))
}

// ListCachedApps returns app IDs that have cached depots.
func (c *DepotCache) ListCachedApps() ([]uint32, error) {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var appIDs []uint32
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var id uint32
		if _, err := fmt.Sscanf(e.Name(), "%d", &id); err == nil {
			appIDs = append(appIDs, id)
		}
	}
	return appIDs, nil
}

// parseManifestFromCache reconstructs a Manifest from the stored binary payload.
// The cached format is the raw ContentManifestPayload protobuf (not the ZIP+sections format from CDN).
func parseManifestFromCache(data []byte, depotID uint32, manifestID uint64, depotKey []byte) (*Manifest, error) {
	payload := &proto.ContentManifestPayload{}
	if err := goproto.Unmarshal(data, payload); err != nil {
		return nil, fmt.Errorf("unmarshal cached manifest: %w", err)
	}

	manifest := &Manifest{
		DepotID:    depotID,
		ManifestID: manifestID,
	}

	for _, mapping := range payload.GetMappings() {
		file, err := convertFileMapping(mapping, depotKey, false)
		if err != nil {
			return nil, err
		}
		manifest.Files = append(manifest.Files, *file)
	}

	return manifest, nil
}
