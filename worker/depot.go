package worker

import (
	"context"
	"log/slog"

	"github.com/warsmite/gamejanitor/steam"
)

// EnsureDepot downloads game files for auth-required Steam games to the worker's local cache.
// Shared implementation used by LocalWorker, ProcessWorker, and the worker Agent.
func EnsureDepot(ctx context.Context, dataDir string, log *slog.Logger, appID uint32, branch, accountName, refreshToken string) (string, error) {
	creds := &staticCredentials{account: accountName, token: refreshToken}
	svc := steam.NewService(log, dataDir, creds)
	defer svc.Close()

	return svc.EnsureDepot(ctx, appID, branch)
}

// staticCredentials provides credentials passed from the controller via gRPC.
type staticCredentials struct {
	account string
	token   string
}

func (c *staticCredentials) SteamAccountName() string  { return c.account }
func (c *staticCredentials) SteamRefreshToken() string { return c.token }
