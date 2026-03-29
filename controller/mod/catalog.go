package mod

import "context"

// ModCatalog is how we discover mods from a source.
// Each source (Modrinth, uMod, Workshop, Thunderstore, CurseForge)
// implements this interface.
type ModCatalog interface {
	Search(ctx context.Context, query string, filters CatalogFilters, offset, limit int) ([]ModResult, int, error)
	GetDetails(ctx context.Context, modID string) (*ModDetails, error)
	GetVersions(ctx context.Context, modID string, filters CatalogFilters) ([]ModVersion, error)
	GetDependencies(ctx context.Context, versionID string) ([]ModDependency, error)
}

const DefaultModLimit = 20

// CatalogFilters carries resolved filter values for catalog queries.
type CatalogFilters struct {
	GameVersion string
	Loader      string
	Extra       map[string]string
	ServerPack  bool // prefer server-specific pack files over client files
}

type ModResult struct {
	SourceID    string `json:"source_id"`
	Source      string `json:"source"` // set by the service, not by the catalog
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Author      string `json:"author"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
	UpdatedAt   string `json:"updated_at"`
}

type ModDetails struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
}

type ModVersion struct {
	VersionID     string   `json:"version_id"`
	Version       string   `json:"version"`
	FileName      string   `json:"file_name"`
	DownloadURL   string   `json:"download_url"`
	GameVersion   string   `json:"game_version"`
	GameVersions  []string `json:"game_versions,omitempty"`
	Loader        string   `json:"loader"`
	HasServerFile bool     `json:"has_server_file,omitempty"`
}

type ModDependency struct {
	ModID     string `json:"mod_id"`
	VersionID string `json:"version_id"`
	Required  bool   `json:"required"`
}

// ModUpdate represents an available update for an installed mod.
type ModUpdate struct {
	ModID          string     `json:"mod_id"`
	ModName        string     `json:"mod_name"`
	CurrentVersion string     `json:"current_version"`
	LatestVersion  ModVersion `json:"latest_version"`
}

// ModIssue represents a compatibility problem with an installed mod.
type ModIssue struct {
	ModID   string `json:"mod_id"`
	ModName string `json:"mod_name"`
	Type    string `json:"type"` // "incompatible", "deactivated"
	Reason  string `json:"reason"`
}

// ModSourceInfo describes an available mod source for a gameserver.
type ModSourceInfo struct {
	Type        string `json:"type"`
	SearchMode  string `json:"search_mode"`
	GameVersion string `json:"game_version,omitempty"`
}
