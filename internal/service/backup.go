package service

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/google/uuid"
)

type BackupService struct {
	db            *sql.DB
	dispatcher    *worker.Dispatcher
	gameserverSvc *GameserverService
	gameStore     *games.GameStore
	store         BackupStore
	settingsSvc   *SettingsService
	log           *slog.Logger
}

func NewBackupService(db *sql.DB, dispatcher *worker.Dispatcher, gameserverSvc *GameserverService, gameStore *games.GameStore, store BackupStore, settingsSvc *SettingsService, log *slog.Logger) *BackupService {
	return &BackupService{db: db, dispatcher: dispatcher, gameserverSvc: gameserverSvc, gameStore: gameStore, store: store, settingsSvc: settingsSvc, log: log}
}

func (s *BackupService) ListBackups(gameserverID string) ([]models.Backup, error) {
	return models.ListBackups(s.db, gameserverID)
}

func (s *BackupService) GetBackup(id string) (*models.Backup, error) {
	return models.GetBackup(s.db, id)
}

func (s *BackupService) CreateBackup(ctx context.Context, gameserverID string, name string) (*models.Backup, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	game := s.gameStore.GetGame(gs.GameID)
	w := s.dispatcher.WorkerFor(gameserverID)

	// Run save-server if game is running and supports it
	if isRunningStatus(gs.Status) && gs.ContainerID != nil && game != nil && HasCapability(game, "save") {
		s.log.Info("running save-server before backup", "gameserver_id", gameserverID)
		exitCode, _, stderr, execErr := w.Exec(ctx, *gs.ContainerID, []string{"/scripts/save-server"})
		if execErr != nil {
			s.log.Warn("save-server exec failed, proceeding with backup", "error", execErr)
		} else if exitCode != 0 {
			s.log.Warn("save-server exited non-zero, proceeding with backup", "exit_code", exitCode, "stderr", stderr)
		}
	}

	// Enforce retention before creating new backup
	if err := s.enforceRetention(ctx, gameserverID); err != nil {
		s.log.Warn("retention enforcement failed, proceeding with backup", "gameserver_id", gameserverID, "error", err)
	}

	backupID := uuid.New().String()
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04:05")
	}

	s.log.Info("creating backup", "gameserver_id", gameserverID, "backup_id", backupID)

	// Get tar stream directly from volume (works whether gameserver is running or stopped)
	tarReader, err := w.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		return nil, fmt.Errorf("backing up volume: %w", err)
	}
	defer tarReader.Close()

	// Pipe gzipped tar through to the store
	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = fmt.Errorf("compressing backup data: %w", err)
			gzWriter.Close()
			pw.CloseWithError(compressErr)
			return
		}
		if err := gzWriter.Close(); err != nil {
			compressErr = fmt.Errorf("closing gzip writer: %w", err)
			pw.CloseWithError(compressErr)
			return
		}
		pw.Close()
	}()

	if err := s.store.Save(ctx, gameserverID, backupID, pr); err != nil {
		return nil, fmt.Errorf("saving backup to store: %w", err)
	}
	if compressErr != nil {
		s.store.Delete(ctx, gameserverID, backupID)
		return nil, compressErr
	}

	sizeBytes, err := s.store.Size(ctx, gameserverID, backupID)
	if err != nil {
		s.log.Warn("failed to get backup size", "backup_id", backupID, "error", err)
	}

	backup := &models.Backup{
		ID:           backupID,
		GameserverID: gameserverID,
		Name:         name,
		SizeBytes:    sizeBytes,
	}
	if err := models.CreateBackup(s.db, backup); err != nil {
		s.store.Delete(ctx, gameserverID, backupID)
		return nil, fmt.Errorf("recording backup in database: %w", err)
	}

	s.log.Info("backup created", "gameserver_id", gameserverID, "backup_id", backupID, "size_bytes", sizeBytes)
	return backup, nil
}

func (s *BackupService) RestoreBackup(ctx context.Context, backupID string) error {
	backup, err := models.GetBackup(s.db, backupID)
	if err != nil {
		return fmt.Errorf("getting backup %s: %w", backupID, err)
	}
	if backup == nil {
		return ErrNotFoundf("backup %s not found", backupID)
	}

	gs, err := models.GetGameserver(s.db, backup.GameserverID)
	if err != nil {
		return fmt.Errorf("getting gameserver %s: %w", backup.GameserverID, err)
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", backup.GameserverID)
	}

	wasRunning := isRunningStatus(gs.Status)

	s.log.Info("restoring backup", "backup_id", backupID, "gameserver_id", gs.ID, "was_running", wasRunning)

	// Stop gameserver if running
	if gs.Status != StatusStopped {
		if err := s.gameserverSvc.Stop(ctx, gs.ID); err != nil {
			return fmt.Errorf("stopping gameserver for restore: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(gs.ID)

	// Load backup from store and decompress
	reader, err := s.store.Load(ctx, backup.GameserverID, backup.ID)
	if err != nil {
		return fmt.Errorf("loading backup from store: %w", err)
	}
	defer reader.Close()

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzReader.Close()

	// Restore directly into volume (clears existing contents and extracts tar)
	if err := w.RestoreVolume(ctx, gs.VolumeName, gzReader); err != nil {
		return fmt.Errorf("restoring backup to volume: %w", err)
	}

	s.log.Info("backup restored", "backup_id", backupID, "gameserver_id", gs.ID)

	if wasRunning {
		s.log.Info("restarting gameserver after restore", "gameserver_id", gs.ID)
		if err := s.gameserverSvc.Start(ctx, gs.ID); err != nil {
			return fmt.Errorf("restarting gameserver after restore: %w", err)
		}
	}

	return nil
}

func (s *BackupService) DeleteBackup(ctx context.Context, backupID string) error {
	backup, err := models.GetBackup(s.db, backupID)
	if err != nil {
		return fmt.Errorf("getting backup %s: %w", backupID, err)
	}
	if backup == nil {
		return ErrNotFoundf("backup %s not found", backupID)
	}

	s.log.Info("deleting backup", "backup_id", backupID, "gameserver_id", backup.GameserverID)

	if err := s.store.Delete(ctx, backup.GameserverID, backup.ID); err != nil {
		return fmt.Errorf("removing backup from store: %w", err)
	}

	return models.DeleteBackup(s.db, backupID)
}

// DeleteBackupsByGameserver removes all backups for a gameserver (DB records + store files).
func (s *BackupService) DeleteBackupsByGameserver(ctx context.Context, gameserverID string) error {
	backups, err := models.ListBackups(s.db, gameserverID)
	if err != nil {
		return fmt.Errorf("listing backups for cleanup: %w", err)
	}

	for _, b := range backups {
		if err := s.store.Delete(ctx, gameserverID, b.ID); err != nil {
			s.log.Warn("failed to remove backup from store during cleanup", "backup_id", b.ID, "error", err)
		}
	}

	return models.DeleteBackupsByGameserver(s.db, gameserverID)
}

// enforceRetention deletes the oldest backups if the gameserver has reached its retention limit.
func (s *BackupService) enforceRetention(ctx context.Context, gameserverID string) error {
	maxBackups := s.settingsSvc.GetMaxBackups()
	if maxBackups <= 0 {
		return nil
	}

	backups, err := models.ListBackups(s.db, gameserverID)
	if err != nil {
		return fmt.Errorf("listing backups for retention: %w", err)
	}

	// ListBackups returns newest first — delete from the end to stay under limit
	// We need to be at maxBackups-1 after this to make room for the new backup
	for len(backups) >= maxBackups {
		oldest := backups[len(backups)-1]
		s.log.Info("retention: deleting oldest backup", "backup_id", oldest.ID, "gameserver_id", gameserverID, "count", len(backups), "max", maxBackups)

		if err := s.store.Delete(ctx, gameserverID, oldest.ID); err != nil {
			s.log.Warn("retention: failed to delete backup file", "backup_id", oldest.ID, "error", err)
		}
		if err := models.DeleteBackup(s.db, oldest.ID); err != nil {
			return fmt.Errorf("retention: deleting backup record: %w", err)
		}

		backups = backups[:len(backups)-1]
	}

	return nil
}
