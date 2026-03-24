package service

import "context"

// ModSearchResult is the common shape returned by all mod sources.
type ModSearchResult struct {
	SourceID    string `json:"source_id"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Author      string `json:"author"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
	UpdatedAt   string `json:"updated_at"`
}

// ModVersion represents a downloadable version of a mod.
type ModVersion struct {
	VersionID    string   `json:"version_id"`
	Version      string   `json:"version"`
	FileName     string   `json:"file_name"`
	DownloadURL  string   `json:"download_url"`
	GameVersion  string   `json:"game_version"`
	GameVersions []string `json:"game_versions,omitempty"`
	Loader       string   `json:"loader"`
}

// ModSourceInfo describes an available mod source for a gameserver, including its search capability.
type ModSourceInfo struct {
	Type        string `json:"type"`
	SearchMode  string `json:"search_mode"`            // "search" or "paste_id"
	GameVersion string `json:"game_version,omitempty"` // resolved server version (e.g., "1.21.1" not "latest")
}

// ModSource abstracts a mod repository (Modrinth, uMod, Steam Workshop).
type ModSource interface {
	// Search returns mods matching a query. gameVersion and loader are optional filters.
	// Returns results, total hit count, and error.
	Search(ctx context.Context, query string, gameVersion string, loader string, offset int, limit int) ([]ModSearchResult, int, error)

	// GetVersions returns available versions for a mod, optionally filtered by game version and loader.
	GetVersions(ctx context.Context, sourceID string, gameVersion string, loader string) ([]ModVersion, error)

	// Download returns the mod file contents for a specific version.
	// Returns file content, filename, and error.
	Download(ctx context.Context, versionID string) ([]byte, string, error)
}
