package worker

import (
	"context"
	"log/slog"

	"github.com/warsmite/gamejanitor/steam"
)

// EnsureDepot downloads game files for Steam games to the worker's local cache.
// Shared implementation used by LocalWorker, ProcessWorker, and the worker Agent.
func EnsureDepot(ctx context.Context, dataDir string, log *slog.Logger, appID uint32, branch, accountName, refreshToken string) (*DepotResult, error) {
	creds := &staticCredentials{account: accountName, token: refreshToken}
	svc := steam.NewService(log, dataDir, creds)
	defer svc.Close()

	result, err := svc.EnsureDepot(ctx, appID, branch)
	if err != nil {
		return nil, err
	}

	return &DepotResult{
		DepotDir:        result.DepotDir,
		Cached:          result.Cached,
		BytesDownloaded: result.BytesDownloaded,
	}, nil
}

// staticCredentials provides credentials passed from the controller via gRPC.
type staticCredentials struct {
	account string
	token   string
}

func (c *staticCredentials) SteamAccountName() string  { return c.account }
func (c *staticCredentials) SteamRefreshToken() string { return c.token }
