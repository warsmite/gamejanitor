package worker

import (
	"context"
	"log/slog"

	"github.com/warsmite/gamejanitor/steam"
)

// EnsureDepot downloads game files for Steam games to the worker's local cache.
// Shared implementation used by LocalWorker, ProcessWorker, and the worker Agent.
func EnsureDepot(ctx context.Context, dataDir string, log *slog.Logger, appID uint32, branch, accountName, refreshToken string, onProgress func(DepotProgress)) (*DepotResult, error) {
	creds := &staticCredentials{account: accountName, token: refreshToken}
	svc := steam.NewService(log, dataDir, creds)
	defer svc.Close()

	var progressFn steam.OnProgressFunc
	if onProgress != nil {
		progressFn = func(completedBytes, totalBytes uint64, completedChunks, totalChunks int) {
			onProgress(DepotProgress{
				CompletedBytes:  completedBytes,
				TotalBytes:      totalBytes,
				CompletedChunks: completedChunks,
				TotalChunks:     totalChunks,
			})
		}
	}

	result, err := svc.EnsureDepot(ctx, appID, branch, progressFn)
	if err != nil {
		return nil, err
	}

	return &DepotResult{
		DepotDir:        result.DepotDir,
		Cached:          result.Cached,
		BytesDownloaded: result.BytesDownloaded,
	}, nil
}

// DownloadWorkshopItem downloads a Steam Workshop UGC item to destDir.
// Uses anonymous login (Workshop content is publicly accessible).
func DownloadWorkshopItem(ctx context.Context, dataDir string, log *slog.Logger, appID uint32, hcontentFile uint64, destDir string) error {
	creds := &staticCredentials{} // anonymous
	svc := steam.NewService(log, dataDir, creds)
	defer svc.Close()

	return svc.DownloadWorkshopItem(ctx, appID, hcontentFile, destDir)
}

// staticCredentials provides credentials passed from the controller via gRPC.
type staticCredentials struct {
	account string
	token   string
}

func (c *staticCredentials) SteamAccountName() string  { return c.account }
func (c *staticCredentials) SteamRefreshToken() string { return c.token }
