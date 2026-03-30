package steam

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
)

// CredentialProvider reads Steam credentials from persistent storage.
// Implemented by the settings service so credentials are always fresh.
type CredentialProvider interface {
	SteamAccountName() string
	SteamRefreshToken() string
}

// Service provides high-level depot download operations for the gameserver lifecycle.
// It manages Steam client connections, caching, and download orchestration.
type Service struct {
	log   *slog.Logger
	cache *DepotCache
	creds CredentialProvider

	client     *Client
	downloader *DepotDownloader
	// lastToken tracks which token was used for the current connection
	// so we reconnect if credentials change.
	lastToken string
	mu        sync.Mutex // protects client/downloader initialization
}

// NewService creates a Steam download service.
// dataDir is the gamejanitor data directory root (cache will be at dataDir/cache/depots/).
func NewService(log *slog.Logger, dataDir string, creds CredentialProvider) *Service {
	cacheDir := filepath.Join(dataDir, "cache", "depots")
	return &Service{
		log:   log.With("component", "steam_service"),
		cache: NewDepotCache(cacheDir, log),
		creds: creds,
	}
}

// HasCredentials returns true if Steam credentials are configured.
func (s *Service) HasCredentials() bool {
	return s.creds.SteamRefreshToken() != ""
}

// EnsureDepot downloads a depot if not already cached or if a newer version is available.
// Returns the path to the cached game files directory, ready to be bind-mounted.
func (s *Service) EnsureDepot(ctx context.Context, appID uint32, branch string) (string, error) {
	if branch == "" {
		branch = "public"
	}

	client, downloader, err := s.getOrConnect(ctx)
	if err != nil {
		return "", err
	}

	// Resolve app info to find depot IDs and current manifest
	appInfo, err := client.GetAppInfo(ctx, appID, branch)
	if err != nil {
		return "", fmt.Errorf("resolve app %d: %w", appID, err)
	}

	if len(appInfo.Depots) == 0 {
		return "", fmt.Errorf("no depots found for app %d branch %q", appID, branch)
	}

	// All depots merge into one directory — games expect a single installation.
	mergedDir := s.cache.AppFilesDir(appID)

	// Download each depot
	for _, depot := range appInfo.Depots {
		// Skip shared depots (depotfromapp) — these reference content from another app
		if depot.DepotFromApp != 0 {
			s.log.Debug("skipping shared depot",
				"depot_id", depot.DepotID,
				"from_app", depot.DepotFromApp,
			)
			continue
		}

		// Only download platform-independent depots and linux depots
		if depot.OSList != "" && !strings.Contains(depot.OSList, "linux") {
			s.log.Debug("skipping non-linux depot",
				"depot_id", depot.DepotID,
				"os", depot.OSList,
			)
			continue
		}

		filesDir := mergedDir

		// Check if already cached with this manifest
		meta := s.cache.GetMeta(appID, depot.DepotID)
		if meta != nil && meta.ManifestID == depot.ManifestID {
			s.log.Info("depot already cached and up-to-date",
				"app_id", appID,
				"depot_id", depot.DepotID,
				"manifest_id", depot.ManifestID,
			)
			continue
		}

		// Get depot key for decryption
		depotKey, err := client.GetDepotDecryptionKey(ctx, depot.DepotID, appID)
		if err != nil {
			return "", fmt.Errorf("get depot key for %d: %w", depot.DepotID, err)
		}

		// Load old manifest for delta update
		var oldManifest *Manifest
		if meta != nil {
			oldManifest = s.cache.GetManifest(appID, depot.DepotID, depotKey)
		}

		result, err := downloader.Download(ctx, DownloadOptions{
			AppID:       appID,
			DepotID:     depot.DepotID,
			Branch:      branch,
			DestDir:     filesDir,
			OldManifest: oldManifest,
			OnProgress: func(p DownloadProgress) {
				if p.CompletedChunks%500 == 0 || p.CompletedChunks == p.TotalChunks {
					pct := float64(p.CompletedChunks) / float64(p.TotalChunks) * 100
					s.log.Info("depot download progress",
						"app_id", appID,
						"depot_id", depot.DepotID,
						"progress", fmt.Sprintf("%.0f%%", pct),
						"chunks", fmt.Sprintf("%d/%d", p.CompletedChunks, p.TotalChunks),
						"downloaded_mb", p.CompletedBytes/1024/1024,
					)
				}
			},
		})
		if err != nil {
			return "", fmt.Errorf("download depot %d: %w", depot.DepotID, err)
		}

		// Save manifest and metadata for future delta updates
		if err := s.cache.SaveMeta(&CacheMeta{
			AppID:      appID,
			DepotID:    depot.DepotID,
			ManifestID: depot.ManifestID,
			BuildID:    appInfo.BuildID,
			Branch:     branch,
		}); err != nil {
			s.log.Warn("failed to save cache metadata", "error", err)
		}

		s.log.Info("depot download complete",
			"app_id", appID,
			"depot_id", depot.DepotID,
			"files_written", result.FilesWritten,
			"is_delta", result.IsDelta,
		)
	}

	return mergedDir, nil
}

// CacheDir returns the path to cached files for an app's first depot.
func (s *Service) CacheDir(appID, depotID uint32) string {
	return s.cache.FilesDir(appID, depotID)
}

func (s *Service) getOrConnect(ctx context.Context) (*Client, *DepotDownloader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token := s.creds.SteamRefreshToken()
	accountName := s.creds.SteamAccountName()

	if token == "" {
		return nil, nil, fmt.Errorf("no Steam credentials configured — run 'gamejanitor steam login'")
	}

	// Reconnect if credentials changed since last connection
	if s.client != nil && s.lastToken != token {
		s.log.Info("Steam credentials changed, reconnecting")
		s.client.Close()
		s.client = nil
		s.downloader = nil
	}

	if s.client != nil && s.downloader != nil {
		return s.client, s.downloader, nil
	}

	client := NewClient(s.log)
	if err := client.Connect(ctx); err != nil {
		return nil, nil, fmt.Errorf("connect to Steam: %w", err)
	}

	if err := client.LoginWithRefreshToken(ctx, accountName, token); err != nil {
		client.Close()
		return nil, nil, fmt.Errorf("Steam login failed: %w", err)
	}

	s.client = client
	s.downloader = NewDepotDownloader(client, s.log)
	s.lastToken = token
	return s.client, s.downloader, nil
}

// Close shuts down the Steam client connection.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		s.client.Close()
		s.client = nil
		s.downloader = nil
	}
}
