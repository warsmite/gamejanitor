package service

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/google/uuid"
)

type BackupService struct {
	db            *sql.DB
	docker        *docker.Client
	gameserverSvc *GameserverService
	dataDir       string
	log           *slog.Logger
}

func NewBackupService(db *sql.DB, dockerClient *docker.Client, gameserverSvc *GameserverService, dataDir string, log *slog.Logger) *BackupService {
	return &BackupService{db: db, docker: dockerClient, gameserverSvc: gameserverSvc, dataDir: dataDir, log: log}
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
	if !isRunningStatus(gs.Status) || gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver must be running to create backup (current status: %s)", gs.Status)
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return nil, fmt.Errorf("getting game for gameserver %s: %w", gameserverID, err)
	}

	// Run save-server if game supports it
	if game != nil && HasCapability(game, "save") {
		s.log.Info("running save-server before backup", "gameserver_id", gameserverID)
		exitCode, _, stderr, execErr := s.docker.Exec(ctx, *gs.ContainerID, []string{"/scripts/save-server"})
		if execErr != nil {
			s.log.Warn("save-server exec failed, proceeding with backup", "error", execErr)
		} else if exitCode != 0 {
			s.log.Warn("save-server exited non-zero, proceeding with backup", "exit_code", exitCode, "stderr", stderr)
		}
	}

	backupID := uuid.New().String()
	if name == "" {
		name = time.Now().Format("2006-01-02 15:04:05")
	}

	backupDir := filepath.Join(s.dataDir, "backups", gameserverID)
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return nil, fmt.Errorf("creating backup directory: %w", err)
	}
	backupPath := filepath.Join(backupDir, backupID+".tar.gz")

	s.log.Info("creating backup", "gameserver_id", gameserverID, "backup_id", backupID, "path", backupPath)

	// Get tar stream of /data from container
	tarReader, err := s.docker.CopyDirFromContainer(ctx, *gs.ContainerID, "/data")
	if err != nil {
		return nil, fmt.Errorf("copying data from container: %w", err)
	}
	defer tarReader.Close()

	// Write gzipped tar to file
	outFile, err := os.Create(backupPath)
	if err != nil {
		return nil, fmt.Errorf("creating backup file: %w", err)
	}
	defer outFile.Close()

	gzWriter := gzip.NewWriter(outFile)
	if _, err := io.Copy(gzWriter, tarReader); err != nil {
		os.Remove(backupPath)
		return nil, fmt.Errorf("writing backup data: %w", err)
	}
	if err := gzWriter.Close(); err != nil {
		os.Remove(backupPath)
		return nil, fmt.Errorf("closing gzip writer: %w", err)
	}
	if err := outFile.Close(); err != nil {
		os.Remove(backupPath)
		return nil, fmt.Errorf("closing backup file: %w", err)
	}

	fi, err := os.Stat(backupPath)
	if err != nil {
		return nil, fmt.Errorf("stat backup file: %w", err)
	}

	backup := &models.Backup{
		ID:           backupID,
		GameserverID: gameserverID,
		Name:         name,
		FilePath:     backupPath,
		SizeBytes:    fi.Size(),
	}
	if err := models.CreateBackup(s.db, backup); err != nil {
		os.Remove(backupPath)
		return nil, fmt.Errorf("recording backup in database: %w", err)
	}

	s.log.Info("backup created", "gameserver_id", gameserverID, "backup_id", backupID, "size_bytes", fi.Size())
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

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return fmt.Errorf("getting game for gameserver %s: %w", gs.ID, err)
	}
	if game == nil {
		return ErrNotFoundf("game %s not found", gs.GameID)
	}

	wasRunning := isRunningStatus(gs.Status)

	s.log.Info("restoring backup", "backup_id", backupID, "gameserver_id", gs.ID, "was_running", wasRunning)

	// Stop gameserver if running
	if gs.Status != StatusStopped {
		if err := s.gameserverSvc.Stop(ctx, gs.ID); err != nil {
			return fmt.Errorf("stopping gameserver for restore: %w", err)
		}
	}

	// Spin up temp container to clear volume and restore
	tempName := "gamejanitor-backup-" + gs.ID
	tempID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       tempName,
		Image:      game.Image,
		Env:        []string{},
		VolumeName: gs.VolumeName,
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return fmt.Errorf("creating temp container for restore: %w", err)
	}
	defer func() {
		if stopErr := s.docker.StopContainer(ctx, tempID, 5); stopErr != nil {
			s.log.Warn("failed to stop temp backup container", "error", stopErr)
		}
		if rmErr := s.docker.RemoveContainer(ctx, tempID); rmErr != nil {
			s.log.Warn("failed to remove temp backup container", "error", rmErr)
		}
	}()

	if err := s.docker.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for restore: %w", err)
	}

	// Clear volume contents
	exitCode, _, stderr, execErr := s.docker.Exec(ctx, tempID, []string{"sh", "-c", "rm -rf /data/* /data/.[!.]* /data/..?*"})
	if execErr != nil {
		return fmt.Errorf("clearing volume: %w", execErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("clearing volume failed (exit %d): %s", exitCode, stderr)
	}

	// Open backup and decompress
	backupFile, err := os.Open(backup.FilePath)
	if err != nil {
		return fmt.Errorf("opening backup file: %w", err)
	}
	defer backupFile.Close()

	gzReader, err := gzip.NewReader(backupFile)
	if err != nil {
		return fmt.Errorf("creating gzip reader: %w", err)
	}
	defer gzReader.Close()

	// Extract tar into /data
	// docker cp extracts the tar at the dest path, but CopyFromContainer wraps /data contents
	// under a "data/" prefix in the tar. We extract to "/" so "data/..." maps to "/data/..."
	if err := s.docker.CopyTarToContainer(ctx, tempID, "/", gzReader); err != nil {
		return fmt.Errorf("extracting backup into container: %w", err)
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

	s.log.Info("deleting backup", "backup_id", backupID, "path", backup.FilePath)

	if err := os.Remove(backup.FilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing backup file: %w", err)
	}

	return models.DeleteBackup(s.db, backupID)
}

// DeleteBackupsByGameserver removes all backups for a gameserver (DB records + files).
func (s *BackupService) DeleteBackupsByGameserver(ctx context.Context, gameserverID string) error {
	backups, err := models.ListBackups(s.db, gameserverID)
	if err != nil {
		return fmt.Errorf("listing backups for cleanup: %w", err)
	}

	for _, b := range backups {
		if err := os.Remove(b.FilePath); err != nil && !os.IsNotExist(err) {
			s.log.Warn("failed to remove backup file during cleanup", "path", b.FilePath, "error", err)
		}
	}

	return models.DeleteBackupsByGameserver(s.db, gameserverID)
}
