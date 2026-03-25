package service

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/google/uuid"
)

type BackupService struct {
	db            *sql.DB
	dispatcher    *worker.Dispatcher
	gameserverSvc *GameserverService
	gameStore     *games.GameStore
	store         BackupStore
	settingsSvc   *SettingsService
	broadcaster   *EventBus
	log           *slog.Logger
}

func NewBackupService(db *sql.DB, dispatcher *worker.Dispatcher, gameserverSvc *GameserverService, gameStore *games.GameStore, store BackupStore, settingsSvc *SettingsService, broadcaster *EventBus, log *slog.Logger) *BackupService {
	return &BackupService{db: db, dispatcher: dispatcher, gameserverSvc: gameserverSvc, gameStore: gameStore, store: store, settingsSvc: settingsSvc, broadcaster: broadcaster, log: log}
}

func (s *BackupService) ListBackups(filter models.BackupFilter) ([]models.Backup, error) {
	return models.ListBackups(s.db, filter)
}

func (s *BackupService) GetBackup(id string) (*models.Backup, error) {
	return models.GetBackup(s.db, id)
}

func (s *BackupService) TotalBackupSize(gameserverID string) (int64, error) {
	return models.TotalBackupSizeByGameserver(s.db, gameserverID)
}

func (s *BackupService) DownloadBackup(ctx context.Context, backupID string) (io.ReadCloser, *models.Backup, error) {
	backup, err := models.GetBackup(s.db, backupID)
	if err != nil {
		return nil, nil, fmt.Errorf("getting backup %s: %w", backupID, err)
	}
	if backup == nil {
		return nil, nil, ErrNotFoundf("backup %s not found", backupID)
	}

	reader, err := s.store.Load(ctx, backup.GameserverID, backup.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading backup from store: %w", err)
	}

	return reader, backup, nil
}

func (s *BackupService) CreateBackup(ctx context.Context, gameserverID string, name string) (*models.Backup, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	// Enforce retention before creating new backup
	if err := s.enforceRetention(ctx, gameserverID); err != nil {
		s.log.Warn("retention enforcement failed, proceeding with backup", "gameserver_id", gameserverID, "error", err)
	}

	backupID := uuid.New().String()
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04:05")
	}

	// Create backup record immediately with in_progress status
	backup := &models.Backup{
		ID:           backupID,
		GameserverID: gameserverID,
		Name:         name,
		Status:       models.BackupStatusInProgress,
	}
	if err := models.CreateBackup(s.db, backup); err != nil {
		return nil, fmt.Errorf("recording backup in database: %w", err)
	}

	actor := ActorFromContext(ctx)
	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupCreate,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gameserverID,
		Backup:       backup,
	})

	s.log.Info("backup initiated", "gameserver_id", gameserverID, "backup_id", backupID)

	// Run the actual backup work in the background
	go s.runBackup(gameserverID, backupID, name, gs, actor)

	return backup, nil
}

func (s *BackupService) runBackup(gameserverID, backupID, name string, gs *models.Gameserver, actor Actor) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	game := s.gameStore.GetGame(gs.GameID)
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		s.failBackup(ctx, gameserverID, backupID, name, actor, "worker unavailable for backup")
		return
	}

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

	// Get tar stream from volume
	tarReader, err := w.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		s.failBackup(ctx, gameserverID, backupID, name, actor, fmt.Sprintf("backing up volume: %v", err))
		return
	}

	// Pipe gzipped tar to store
	pr, pw := io.Pipe()
	var compressErr error
	go func() {
		gzWriter := gzip.NewWriter(pw)
		if _, err := io.Copy(gzWriter, tarReader); err != nil {
			compressErr = fmt.Errorf("compressing backup data: %w", err)
			gzWriter.Close()
			pw.CloseWithError(compressErr)
			tarReader.Close()
			return
		}
		tarReader.Close()
		if err := gzWriter.Close(); err != nil {
			compressErr = fmt.Errorf("closing gzip writer: %w", err)
			pw.CloseWithError(compressErr)
			return
		}
		pw.Close()
	}()

	if err := s.store.Save(ctx, gameserverID, backupID, pr); err != nil {
		s.failBackup(ctx, gameserverID, backupID, name, actor, fmt.Sprintf("saving to store: %v", err))
		return
	}
	if compressErr != nil {
		s.store.Delete(ctx, gameserverID, backupID)
		s.failBackup(ctx, gameserverID, backupID, name, actor, compressErr.Error())
		return
	}

	sizeBytes, err := s.store.Size(ctx, gameserverID, backupID)
	if err != nil {
		s.log.Warn("failed to get backup size", "backup_id", backupID, "error", err)
	}

	// Mark completed
	if err := models.UpdateBackupStatus(s.db, backupID, models.BackupStatusCompleted, sizeBytes, ""); err != nil {
		s.log.Error("failed to mark backup completed", "backup_id", backupID, "error", err)
		return
	}

	s.log.Info("backup completed", "gameserver_id", gameserverID, "backup_id", backupID, "size_bytes", sizeBytes)

	completedBackup, _ := models.GetBackup(s.db, backupID)
	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupCompleted,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gameserverID,
		Backup:       completedBackup,
	})
}

func (s *BackupService) failBackup(ctx context.Context, gameserverID, backupID, name string, actor Actor, reason string) {
	s.log.Error("backup failed", "gameserver_id", gameserverID, "backup_id", backupID, "error", reason)

	// Clean up partial data
	if err := s.store.Delete(ctx, gameserverID, backupID); err != nil {
		s.log.Warn("failed to clean up partial backup data", "backup_id", backupID, "error", err)
	}

	// Mark failed in DB
	if err := models.UpdateBackupStatus(s.db, backupID, models.BackupStatusFailed, 0, reason); err != nil {
		s.log.Error("failed to mark backup as failed", "backup_id", backupID, "error", err)
	}

	failedBackup, _ := models.GetBackup(s.db, backupID)
	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupFailed,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gameserverID,
		Backup:       failedBackup,
		Error:        reason,
	})
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

	actor := ActorFromContext(ctx)
	wasRunning := isRunningStatus(gs.Status)

	s.log.Info("restore initiated", "backup_id", backupID, "gameserver_id", gs.ID, "was_running", wasRunning)

	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupRestore,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gs.ID,
		Backup:       backup,
	})

	go s.runRestore(gs.ID, backupID, backup.Name, gs.VolumeName, wasRunning, actor)

	return nil
}

func (s *BackupService) runRestore(gameserverID, backupID, backupName, volumeName string, wasRunning bool, actor Actor) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Stop gameserver if running
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil || gs == nil {
		s.failRestore(gameserverID, backupID, backupName, actor, "gameserver not found")
		return
	}
	if gs.Status != StatusStopped {
		if err := s.gameserverSvc.Stop(ctx, gameserverID); err != nil {
			s.failRestore(gameserverID, backupID, backupName, actor, fmt.Sprintf("stopping gameserver: %v", err))
			return
		}
	}

	w := s.dispatcher.WorkerFor(gameserverID)

	// Load backup from store and decompress
	reader, err := s.store.Load(ctx, gameserverID, backupID)
	if err != nil {
		s.failRestore(gameserverID, backupID, backupName, actor, fmt.Sprintf("loading backup from store: %v", err))
		return
	}

	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		reader.Close()
		s.failRestore(gameserverID, backupID, backupName, actor, fmt.Sprintf("decompressing backup: %v", err))
		return
	}

	if err := w.RestoreVolume(ctx, volumeName, gzReader); err != nil {
		gzReader.Close()
		reader.Close()
		s.failRestore(gameserverID, backupID, backupName, actor, fmt.Sprintf("restoring volume: %v", err))
		return
	}
	gzReader.Close()
	reader.Close()

	s.log.Info("backup restored", "backup_id", backupID, "gameserver_id", gameserverID)

	restoredBackup, _ := models.GetBackup(s.db, backupID)
	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupRestoreCompleted,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gameserverID,
		Backup:       restoredBackup,
	})

	if wasRunning {
		s.log.Info("restarting gameserver after restore", "gameserver_id", gameserverID)
		if err := s.gameserverSvc.Start(ctx, gameserverID); err != nil {
			s.log.Error("failed to restart after restore", "gameserver_id", gameserverID, "error", err)
			s.broadcaster.Publish(GameserverErrorEvent{GameserverID: gameserverID, Reason: fmt.Sprintf("Restart after restore failed: %v", err), Timestamp: time.Now()})
		}
	} else {
		s.broadcaster.Publish(ContainerStoppedEvent{GameserverID: gameserverID, Timestamp: time.Now()})
	}
}

func (s *BackupService) failRestore(gameserverID, backupID, backupName string, actor Actor, reason string) {
	s.log.Error("backup restore failed", "gameserver_id", gameserverID, "backup_id", backupID, "error", reason)
	failedRestoreBackup, _ := models.GetBackup(s.db, backupID)
	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupRestoreFailed,
		Timestamp:    time.Now(),
		Actor:        actor,
		GameserverID: gameserverID,
		Backup:       failedRestoreBackup,
		Error:        reason,
	})
	s.broadcaster.Publish(GameserverErrorEvent{GameserverID: gameserverID, Reason: operationFailedReason("Backup restore failed", fmt.Errorf("%s", reason)), Timestamp: time.Now()})
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

	if err := models.DeleteBackup(s.db, backupID); err != nil {
		return fmt.Errorf("deleting backup record: %w", err)
	}

	if err := s.store.Delete(ctx, backup.GameserverID, backup.ID); err != nil {
		s.log.Warn("backup record deleted but store file removal failed", "backup_id", backupID, "error", err)
	}

	s.broadcaster.Publish(BackupActionEvent{
		Type:         EventBackupDelete,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: backup.GameserverID,
		Backup:       backup,
	})

	return nil
}

// DeleteBackupsByGameserver removes all backups for a gameserver (DB records + store files).
func (s *BackupService) DeleteBackupsByGameserver(ctx context.Context, gameserverID string) error {
	backups, err := models.ListBackups(s.db, models.BackupFilter{GameserverID: gameserverID})
	if err != nil {
		return fmt.Errorf("listing backups for cleanup: %w", err)
	}

	if err := models.DeleteBackupsByGameserver(s.db, gameserverID); err != nil {
		return fmt.Errorf("deleting backup records: %w", err)
	}

	for _, b := range backups {
		if err := s.store.Delete(ctx, gameserverID, b.ID); err != nil {
			s.log.Warn("backup record deleted but store file removal failed", "backup_id", b.ID, "error", err)
		}
	}

	return nil
}

// enforceRetention deletes the oldest backups if the gameserver has reached its retention limit.
func (s *BackupService) enforceRetention(ctx context.Context, gameserverID string) error {
	maxBackups := s.settingsSvc.GetInt(SettingMaxBackups)

	// Per-gameserver override takes precedence over global setting
	if gs, err := models.GetGameserver(s.db, gameserverID); err == nil && gs != nil && gs.BackupLimit != nil {
		maxBackups = *gs.BackupLimit
	}

	if maxBackups <= 0 {
		return nil
	}

	backups, err := models.ListBackups(s.db, models.BackupFilter{GameserverID: gameserverID})
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
