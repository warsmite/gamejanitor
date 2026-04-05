package steam

import (
	"context"
	"fmt"
	"log/slog"
	"os"
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

// EnsureDepotResult contains the outcome of an EnsureDepot call.
type EnsureDepotResult struct {
	DepotDir        string
	Cached          bool   // true if all depots were already up-to-date
	BytesDownloaded uint64
}

// OnProgressFunc is called during depot downloads with progress updates.
type OnProgressFunc func(completedBytes, totalBytes uint64, completedChunks, totalChunks int)

// EnsureDepot downloads a depot if not already cached or if a newer version is available.
// Returns the path to the cached game files directory, ready to be bind-mounted.
// onProgress is called during download. May be nil.
func (s *Service) EnsureDepot(ctx context.Context, appID uint32, branch string, onProgress OnProgressFunc) (*EnsureDepotResult, error) {
	if branch == "" {
		branch = "public"
	}

	client, downloader, err := s.getOrConnect(ctx)
	if err != nil {
		return nil, err
	}

	// Resolve app info to find depot IDs and current manifest
	appInfo, err := client.GetAppInfo(ctx, appID, branch)
	if err != nil {
		return nil, fmt.Errorf("resolve app %d: %w", appID, err)
	}

	if len(appInfo.Depots) == 0 {
		return nil, fmt.Errorf("no depots found for app %d branch %q", appID, branch)
	}

	// All depots merge into one directory — games expect a single installation.
	// Downloads go to a temp directory first, then atomically renamed to the
	// final path on success. This prevents corrupted game files if the process
	// dies mid-download.
	mergedDir := s.cache.AppFilesDir(appID)
	stagingDir := mergedDir + ".staging"

	// Clean up any leftover staging dir from a previous failed download
	os.RemoveAll(stagingDir)

	// Download each depot
	allCached := true
	needsStaging := false
	var totalBytesDownloaded uint64
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

		needsStaging = true

		// Get depot key for decryption
		depotKey, err := client.GetDepotDecryptionKey(ctx, depot.DepotID, appID)
		if err != nil {
			return nil, fmt.Errorf("get depot key for %d: %w", depot.DepotID, err)
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
			DestDir:     stagingDir,
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
				if onProgress != nil {
					onProgress(p.CompletedBytes, p.TotalBytes, p.CompletedChunks, p.TotalChunks)
				}
			},
		})
		if err != nil {
			return nil, fmt.Errorf("download depot %d: %w", depot.DepotID, err)
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

		allCached = false
		totalBytesDownloaded += result.BytesDownloaded

		s.log.Info("depot download complete",
			"app_id", appID,
			"depot_id", depot.DepotID,
			"files_written", result.FilesWritten,
			"is_delta", result.IsDelta,
		)
	}

	// Atomic swap: staging → merged. Only if we actually downloaded something.
	if needsStaging {
		// Remove old merged dir (if exists) and rename staging into place
		if err := os.RemoveAll(mergedDir); err != nil {
			return nil, fmt.Errorf("remove old cache: %w", err)
		}
		if err := os.Rename(stagingDir, mergedDir); err != nil {
			return nil, fmt.Errorf("finalize cache: %w", err)
		}
		s.log.Info("depot cache finalized", "app_id", appID, "path", mergedDir)
	}

	return &EnsureDepotResult{
		DepotDir:        mergedDir,
		Cached:          allCached,
		BytesDownloaded: totalBytesDownloaded,
	}, nil
}

// DownloadWorkshopItem downloads a Workshop UGC item to destDir.
// appID is the game's app ID, publishedFileID is the Workshop item ID.
// hcontentFile is the UGC content handle from GetPublishedFileDetails API.
func (s *Service) DownloadWorkshopItem(ctx context.Context, appID uint32, hcontentFile uint64, destDir string) error {
	client, downloader, err := s.getOrConnect(ctx)
	if err != nil {
		return err
	}

	// Get app info to find the workshop depot ID
	appInfo, err := client.GetAppInfo(ctx, appID, "public")
	if err != nil {
		return fmt.Errorf("get app info for workshop: %w", err)
	}

	// Use workshopdepot if available, otherwise fall back to app ID
	workshopDepotID := appInfo.WorkshopDepotID
	if workshopDepotID == 0 {
		workshopDepotID = appID
	}

	s.log.Info("downloading workshop item",
		"app_id", appID,
		"workshop_depot", workshopDepotID,
		"hcontent_file", hcontentFile,
	)

	return downloader.DownloadUGCItem(ctx, appID, workshopDepotID, hcontentFile, destDir)
}

func (s *Service) getOrConnect(ctx context.Context) (*Client, *DepotDownloader, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	token := s.creds.SteamRefreshToken()
	accountName := s.creds.SteamAccountName()

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

	if token != "" {
		if err := client.LoginWithRefreshToken(ctx, accountName, token); err != nil {
			client.Close()
			return nil, nil, fmt.Errorf("Steam login failed: %w", err)
		}
	} else {
		if err := client.LoginAnonymous(ctx); err != nil {
			client.Close()
			return nil, nil, fmt.Errorf("Steam anonymous login failed: %w", err)
		}
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
