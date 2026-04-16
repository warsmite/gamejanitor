package local

import (
	"context"
	"path/filepath"

	"github.com/warsmite/gamejanitor/worker"
)

// --- Worker interface: Game scripts & Steam ---

func (w *LocalWorker) PrepareGameScripts(ctx context.Context, gameID, gameserverID string) (string, string, error) {
	return PrepareGameScripts(w.gameStore, w.dataDir, gameID, gameserverID)
}

func (w *LocalWorker) EnsureDepot(ctx context.Context, appID uint32, branch, accountName, refreshToken string, onProgress func(worker.DepotProgress)) (*worker.DepotResult, error) {
	return EnsureDepot(ctx, w.dataDir, w.log, appID, branch, accountName, refreshToken, onProgress)
}

func (w *LocalWorker) CopyDepotToVolume(ctx context.Context, depotDir string, volumeName string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return CopyDepotToVolume(depotDir, mountpoint)
}

func (w *LocalWorker) DownloadWorkshopItem(ctx context.Context, volumeName string, appID uint32, hcontentFile uint64, installPath string) error {
	mountpoint, err := w.resolve(ctx, volumeName)
	if err != nil {
		return err
	}
	return DownloadWorkshopItem(ctx, w.dataDir, w.log, appID, hcontentFile, filepath.Join(mountpoint, installPath))
}
