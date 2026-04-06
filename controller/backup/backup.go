package backup

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// Store abstracts all database operations the backup service needs.
type Store interface {
	ListBackups(filter model.BackupFilter) ([]model.Backup, error)
	GetBackup(id string) (*model.Backup, error)
	CreateBackup(b *model.Backup) error
	UpdateBackup(b *model.Backup) error
	DeleteBackup(id string) error
	DeleteBackupsByGameserver(gameserverID string) error
	TotalBackupSizeByGameserver(gameserverID string) (int64, error)
	GetGameserver(id string) (*model.Gameserver, error)
}

// GameserverLifecycle is a narrow interface for gameserver operations needed by backup.
type GameserverLifecycle interface {
	Stop(ctx context.Context, id string) error
	Start(ctx context.Context, id string, onProgress operation.ProgressFunc) error
	GetGameserver(id string) (*model.Gameserver, error)
}

// ActivityTracker records the lifecycle of long-running activities.
type ActivityTracker interface {
	Start(gameserverID, workerID, activityType string, actor json.RawMessage, data json.RawMessage) (string, error)
	Complete(activityID string)
	Fail(activityID string, reason error)
}

// maxConcurrentBackups limits how many backup/restore operations can run simultaneously.
// Prevents CPU saturation (gzip) and memory pressure (tar streaming) under load.
// Additional backups queue until a slot opens (up to the operation timeout).
const maxConcurrentBackups = 3

type BackupService struct {
	store         Store
	dispatcher    *orchestrator.Dispatcher
	gameserverSvc GameserverLifecycle
	gameStore     *games.GameStore
	storage       Storage
	settingsSvc   *settings.SettingsService
	broadcaster   *event.EventBus
	activity      ActivityTracker
	log           *slog.Logger
	sem           chan struct{} // concurrency limiter for backup/restore goroutines
}

func (s *BackupService) Storage() Storage {
	return s.storage
}

func (s *BackupService) SetActivityTracker(tracker ActivityTracker) {
	s.activity = tracker
}

func (s *BackupService) startActivity(gsID, workerID, activityType string, data json.RawMessage) string {
	if s.activity == nil {
		return ""
	}
	activityID, err := s.activity.Start(gsID, workerID, activityType, nil, data)
	if err != nil {
		s.log.Warn("failed to start activity tracking", "type", activityType, "error", err)
		return ""
	}
	return activityID
}

func (s *BackupService) completeActivity(gameserverID string) {
	if s.activity != nil && gameserverID != "" {
		s.activity.Complete(gameserverID)
	}
}

func (s *BackupService) failActivityRecord(gameserverID string, reason error) {
	if s.activity != nil && gameserverID != "" {
		s.activity.Fail(gameserverID, reason)
	}
}

func NewBackupService(store Store, dispatcher *orchestrator.Dispatcher, gameserverSvc GameserverLifecycle, gameStore *games.GameStore, storage Storage, settingsSvc *settings.SettingsService, broadcaster *event.EventBus, log *slog.Logger) *BackupService {
	return &BackupService{
		store:         store,
		dispatcher:    dispatcher,
		gameserverSvc: gameserverSvc,
		gameStore:     gameStore,
		storage:       storage,
		settingsSvc:   settingsSvc,
		broadcaster:   broadcaster,
		log:           log,
		sem:           make(chan struct{}, maxConcurrentBackups),
	}
}

func (s *BackupService) ListBackups(filter model.BackupFilter) ([]model.Backup, error) {
	return s.store.ListBackups(filter)
}

func (s *BackupService) GetBackup(gameserverID, backupID string) (*model.Backup, error) {
	return s.getBackupForGameserver(gameserverID, backupID)
}

// getBackupForGameserver fetches a backup and verifies it belongs to the expected gameserver.
// Returns ErrNotFound if the backup doesn't exist or belongs to a different gameserver.
func (s *BackupService) getBackupForGameserver(gameserverID, backupID string) (*model.Backup, error) {
	backup, err := s.store.GetBackup(backupID)
	if err != nil {
		return nil, fmt.Errorf("getting backup %s: %w", backupID, err)
	}
	if backup == nil || backup.GameserverID != gameserverID {
		return nil, controller.ErrNotFoundf("backup %s not found", backupID)
	}
	return backup, nil
}

func (s *BackupService) TotalBackupSize(gameserverID string) (int64, error) {
	return s.store.TotalBackupSizeByGameserver(gameserverID)
}

func (s *BackupService) DownloadBackup(ctx context.Context, gameserverID, backupID string) (io.ReadCloser, *model.Backup, error) {
	backup, err := s.getBackupForGameserver(gameserverID, backupID)
	if err != nil {
		return nil, nil, err
	}

	reader, err := s.storage.Load(ctx, backup.GameserverID, backup.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("loading backup from store: %w", err)
	}

	return reader, backup, nil
}

func (s *BackupService) CreateBackup(ctx context.Context, gameserverID string, name string) (*model.Backup, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	// Enforce retention before creating new backup
	if err := s.enforceRetention(ctx, gameserverID); err != nil {
		s.log.Warn("retention enforcement failed, proceeding with backup", "gameserver", gameserverID, "error", err)
	}

	backupID := uuid.New().String()
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04:05")
	}

	// Create backup record — status is derived from the activity table,
	// but we set it on the returned struct since the activity hasn't started yet
	backup := &model.Backup{
		ID:           backupID,
		GameserverID: gameserverID,
		Name:         name,
		Status:       model.BackupStatusInProgress,
	}
	if err := s.store.CreateBackup(backup); err != nil {
		return nil, fmt.Errorf("recording backup in database: %w", err)
	}

	actor := event.ActorFromContext(ctx)
	s.broadcaster.Publish(event.NewEvent(event.EventBackupCreate, gameserverID, actor, &event.BackupActionData{
		Backup: backup,
	}))

	s.log.Info("backup initiated", "gameserver", gameserverID, "backup", backupID)

	// Run the actual backup work in the background
	go s.runBackup(gameserverID, backupID, name, gs, actor)

	return backup, nil
}

func (s *BackupService) runBackup(gameserverID, backupID, name string, gs *model.Gameserver, actor event.Actor) {
	// No operation timeout. Game server volumes can be 500GB+ and backup duration
	// depends on disk speed, compression ratio, and upload bandwidth. An arbitrary
	// timeout would corrupt large backups mid-stream. Concurrency is bounded by
	// the semaphore (maxConcurrentBackups), and the process-level shutdown signal
	// is the only external cancellation path.
	ctx := context.Background()

	// Wait for a backup slot (up to 10 minutes in the queue).
	// Queued backups show as "in progress" to the user and start once a slot opens.
	queueCtx, queueCancel := context.WithTimeout(ctx, 10*time.Minute)
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-queueCtx.Done():
		queueCancel()
		s.failBackup(ctx, gameserverID, backupID, name, actor, "backup queue timeout — too many concurrent backups")
		return
	}
	queueCancel()

	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	backupMetaJSON, _ := json.Marshal(map[string]string{"backup_id": backupID})
	s.startActivity(gameserverID, workerID, model.OpBackup, backupMetaJSON)

	game := s.gameStore.GetGame(gs.GameID)
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		s.failActivityRecord(gameserverID, fmt.Errorf("worker unavailable"))
		s.failBackup(ctx, gameserverID, backupID, name, actor, "worker unavailable for backup")
		return
	}

	// Run save-server if instance is running and game supports it
	if gs.InstanceID != nil && game != nil && game.HasCapability("save") {
		s.log.Info("running save-server before backup", "gameserver", gameserverID)
		exitCode, _, stderr, execErr := w.Exec(ctx, *gs.InstanceID, []string{"/scripts/save-server"})
		if execErr != nil {
			s.log.Warn("save-server exec failed, proceeding with backup", "error", execErr)
		} else if exitCode != 0 {
			s.log.Warn("save-server exited non-zero, proceeding with backup", "exit_code", exitCode, "stderr", stderr)
		}
	}

	// Get tar stream from volume
	tarReader, err := w.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		s.failActivityRecord(gameserverID, err)
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

	if err := s.storage.Save(ctx, gameserverID, backupID, pr); err != nil {
		s.failActivityRecord(gameserverID, err)
		s.failBackup(ctx, gameserverID, backupID, name, actor, fmt.Sprintf("saving to store: %v", err))
		return
	}
	if compressErr != nil {
		s.storage.Delete(ctx, gameserverID, backupID)
		s.failActivityRecord(gameserverID, compressErr)
		s.failBackup(ctx, gameserverID, backupID, name, actor, compressErr.Error())
		return
	}

	sizeBytes, err := s.storage.Size(ctx, gameserverID, backupID)
	if err != nil {
		s.log.Warn("failed to get backup size", "backup", backupID, "error", err)
	}

	if b, err := s.store.GetBackup(backupID); err == nil && b != nil {
		b.SizeBytes = sizeBytes
		b.Status = model.BackupStatusCompleted
		if err := s.store.UpdateBackup(b); err != nil {
			s.log.Error("failed to update backup", "backup", backupID, "error", err)
		}
	}

	s.completeActivity(gameserverID)
	s.log.Info("backup completed", "gameserver", gameserverID, "backup", backupID, "size_bytes", sizeBytes)

	completedBackup, _ := s.store.GetBackup(backupID)
	s.broadcaster.Publish(event.NewEvent(event.EventBackupCompleted, gameserverID, actor, &event.BackupActionData{
		Backup: completedBackup,
	}))
}

func (s *BackupService) failBackup(ctx context.Context, gameserverID, backupID, name string, actor event.Actor, reason string) {
	s.log.Error("backup failed", "gameserver", gameserverID, "backup", backupID, "error", reason)

	// Clean up partial data
	if err := s.storage.Delete(ctx, gameserverID, backupID); err != nil {
		s.log.Warn("failed to clean up partial backup data", "backup", backupID, "error", err)
	}

	// Update backup status to failed
	if b, err := s.store.GetBackup(backupID); err == nil && b != nil {
		b.Status = model.BackupStatusFailed
		s.store.UpdateBackup(b)
	}

	failedBackup, _ := s.store.GetBackup(backupID)
	s.broadcaster.Publish(event.NewEvent(event.EventBackupFailed, gameserverID, actor, &event.BackupActionData{
		Backup: failedBackup,
		Error:  reason,
	}))
}

func (s *BackupService) RestoreBackup(ctx context.Context, gameserverID, backupID string) error {
	backup, err := s.getBackupForGameserver(gameserverID, backupID)
	if err != nil {
		return err
	}

	gs, err := s.store.GetGameserver(backup.GameserverID)
	if err != nil {
		return fmt.Errorf("getting gameserver %s: %w", backup.GameserverID, err)
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", backup.GameserverID)
	}

	actor := event.ActorFromContext(ctx)
	wasRunning := gs.InstanceID != nil

	// Register the operation before launching the goroutine so it's
	// immediately visible via the API (callers can poll operation_type).
	workerID := ""
	if gs.NodeID != nil {
		workerID = *gs.NodeID
	}
	restoreMetaJSON, _ := json.Marshal(map[string]string{"backup_id": backupID})
	s.startActivity(gs.ID, workerID, model.OpRestore, restoreMetaJSON)

	s.log.Info("restore initiated", "backup", backupID, "gameserver", gs.ID, "was_running", wasRunning)

	s.broadcaster.Publish(event.NewEvent(event.EventBackupRestore, gs.ID, actor, &event.BackupActionData{
		Backup: backup,
	}))

	go s.runRestore(gs.ID, backupID, backup.Name, gs.VolumeName, wasRunning, actor)

	return nil
}

func (s *BackupService) runRestore(gameserverID, backupID, backupName, volumeName string, wasRunning bool, actor event.Actor) {
	// No operation timeout — same rationale as runBackup. Large restores (500GB+)
	// must run to completion. Concurrency is bounded by the semaphore.
	ctx := context.Background()

	queueCtx, queueCancel := context.WithTimeout(ctx, 10*time.Minute)
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-queueCtx.Done():
		queueCancel()
		s.failRestore(gameserverID, backupID, backupName, actor, "restore queue timeout — too many concurrent backups")
		return
	}
	queueCancel()

	opSucceeded := false
	defer func() {
		if opSucceeded {
			s.completeActivity(gameserverID)
		} else {
			s.failActivityRecord(gameserverID, fmt.Errorf("restore failed"))
		}
	}()

	if wasRunning {
		if err := s.gameserverSvc.Stop(ctx, gameserverID); err != nil {
			s.failRestore(gameserverID, backupID, backupName, actor, fmt.Sprintf("stopping gameserver: %v", err))
			return
		}
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		s.failRestore(gameserverID, backupID, backupName, actor, "worker unavailable for restore")
		return
	}

	// Load backup from store and decompress
	reader, err := s.storage.Load(ctx, gameserverID, backupID)
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

	opSucceeded = true
	s.log.Info("backup restored", "backup", backupID, "gameserver", gameserverID)

	restoredBackup, _ := s.store.GetBackup(backupID)
	s.broadcaster.Publish(event.NewEvent(event.EventBackupRestoreCompleted, gameserverID, actor, &event.BackupActionData{
		Backup: restoredBackup,
	}))

	if wasRunning {
		s.log.Info("restarting gameserver after restore", "gameserver", gameserverID)
		if err := s.gameserverSvc.Start(ctx, gameserverID, nil); err != nil {
			s.log.Error("failed to restart after restore", "gameserver", gameserverID, "error", err)
			s.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, gameserverID, &event.ErrorData{Reason: fmt.Sprintf("Restart after restore failed: %v", err)}))
		}
	} else {
		s.broadcaster.Publish(event.NewSystemEvent(event.EventInstanceStopped, gameserverID, nil))
	}
}

func (s *BackupService) failRestore(gameserverID, backupID, backupName string, actor event.Actor, reason string) {
	s.log.Error("backup restore failed", "gameserver", gameserverID, "backup", backupID, "error", reason)
	failedRestoreBackup, _ := s.store.GetBackup(backupID)
	s.broadcaster.Publish(event.NewEvent(event.EventBackupRestoreFailed, gameserverID, actor, &event.BackupActionData{
		Backup: failedRestoreBackup,
		Error:  reason,
	}))
	s.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, gameserverID, &event.ErrorData{Reason: controller.OperationFailedReason("Backup restore failed", fmt.Errorf("%s", reason))}))
}

func (s *BackupService) DeleteBackup(ctx context.Context, gameserverID, backupID string) error {
	backup, err := s.getBackupForGameserver(gameserverID, backupID)
	if err != nil {
		return err
	}

	s.log.Info("deleting backup", "backup", backupID, "gameserver", backup.GameserverID)

	if err := s.store.DeleteBackup(backupID); err != nil {
		return fmt.Errorf("deleting backup record: %w", err)
	}

	if err := s.storage.Delete(ctx, backup.GameserverID, backup.ID); err != nil {
		s.log.Warn("backup record deleted but store file removal failed", "backup", backupID, "error", err)
	}

	s.broadcaster.Publish(event.NewEvent(event.EventBackupDelete, backup.GameserverID, event.ActorFromContext(ctx), &event.BackupActionData{
		Backup: backup,
	}))

	return nil
}

// DeleteBackupsByGameserver removes all backups for a gameserver (DB records + store files).
func (s *BackupService) DeleteBackupsByGameserver(ctx context.Context, gameserverID string) error {
	backups, err := s.store.ListBackups(model.BackupFilter{GameserverID: gameserverID})
	if err != nil {
		return fmt.Errorf("listing backups for cleanup: %w", err)
	}

	if err := s.store.DeleteBackupsByGameserver(gameserverID); err != nil {
		return fmt.Errorf("deleting backup records: %w", err)
	}

	for _, b := range backups {
		if err := s.storage.Delete(ctx, gameserverID, b.ID); err != nil {
			s.log.Warn("backup record deleted but store file removal failed", "backup", b.ID, "error", err)
		}
	}

	return nil
}

// enforceRetention deletes the oldest backups if the gameserver has reached its retention limit.
func (s *BackupService) enforceRetention(ctx context.Context, gameserverID string) error {
	maxBackups := s.settingsSvc.GetInt(settings.SettingMaxBackups)

	// Per-gameserver override takes precedence over global setting
	if gs, err := s.store.GetGameserver(gameserverID); err == nil && gs != nil && gs.BackupLimit != nil {
		maxBackups = *gs.BackupLimit
	}

	if maxBackups <= 0 {
		return nil
	}

	backups, err := s.store.ListBackups(model.BackupFilter{GameserverID: gameserverID})
	if err != nil {
		return fmt.Errorf("listing backups for retention: %w", err)
	}

	// ListBackups returns newest first — delete from the end to stay under limit
	// We need to be at maxBackups-1 after this to make room for the new backup
	for len(backups) >= maxBackups {
		oldest := backups[len(backups)-1]
		s.log.Info("retention: deleting oldest backup", "backup", oldest.ID, "gameserver", gameserverID, "count", len(backups), "max", maxBackups)

		if err := s.storage.Delete(ctx, gameserverID, oldest.ID); err != nil {
			s.log.Warn("retention: failed to delete backup file", "backup", oldest.ID, "error", err)
		}
		if err := s.store.DeleteBackup(oldest.ID); err != nil {
			return fmt.Errorf("retention: deleting backup record: %w", err)
		}

		backups = backups[:len(backups)-1]
	}

	return nil
}
