package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
)

type FileService struct {
	db         *sql.DB
	dispatcher *worker.Dispatcher
	log        *slog.Logger
}

func NewFileService(db *sql.DB, dispatcher *worker.Dispatcher, log *slog.Logger) *FileService {
	return &FileService{
		db:         db,
		dispatcher: dispatcher,
		log:        log,
	}
}

func (s *FileService) ListDirectory(ctx context.Context, gameserverID string, dirPath string) ([]worker.FileEntry, error) {
	dirPath, err := validatePath(dirPath)
	if err != nil {
		return nil, err
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}

	// Map /data/... to volume-relative path
	relPath := strings.TrimPrefix(dirPath, "/data")
	if relPath == "" {
		relPath = "/"
	}

	return s.dispatcher.WorkerFor(gameserverID).ListFiles(ctx, gs.VolumeName, relPath)
}

func (s *FileService) ReadFile(ctx context.Context, gameserverID string, filePath string) ([]byte, error) {
	filePath, err := validatePath(filePath)
	if err != nil {
		return nil, err
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}

	relPath := strings.TrimPrefix(filePath, "/data")
	return s.dispatcher.WorkerFor(gameserverID).ReadFile(ctx, gs.VolumeName, relPath)
}

func (s *FileService) WriteFile(ctx context.Context, gameserverID string, filePath string, content []byte) error {
	filePath, err := validatePath(filePath)
	if err != nil {
		return err
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	if gs.MaxStorageMB != nil {
		w := s.dispatcher.WorkerFor(gameserverID)
		volSize, err := w.VolumeSize(ctx, gs.VolumeName)
		if err != nil {
			s.log.Warn("failed to check volume size before write", "gameserver_id", gameserverID, "error", err)
		} else if volSize >= int64(*gs.MaxStorageMB)*1024*1024 {
			return fmt.Errorf("storage limit exceeded (using %d MB of %d MB)", volSize/1024/1024, *gs.MaxStorageMB)
		}
	}

	relPath := strings.TrimPrefix(filePath, "/data")
	return s.dispatcher.WorkerFor(gameserverID).WriteFile(ctx, gs.VolumeName, relPath, content, 0644)
}

func (s *FileService) DeletePath(ctx context.Context, gameserverID string, targetPath string) error {
	targetPath, err := validatePath(targetPath)
	if err != nil {
		return err
	}
	if targetPath == "/data" {
		return fmt.Errorf("cannot delete the root data directory")
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	relPath := strings.TrimPrefix(targetPath, "/data")
	return s.dispatcher.WorkerFor(gameserverID).DeletePath(ctx, gs.VolumeName, relPath)
}

func (s *FileService) CreateDirectory(ctx context.Context, gameserverID string, dirPath string) error {
	dirPath, err := validatePath(dirPath)
	if err != nil {
		return err
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	relPath := strings.TrimPrefix(dirPath, "/data")
	return s.dispatcher.WorkerFor(gameserverID).CreateDirectory(ctx, gs.VolumeName, relPath)
}

func (s *FileService) RenamePath(ctx context.Context, gameserverID string, oldPath string, newPath string) error {
	oldPath, err := validatePath(oldPath)
	if err != nil {
		return err
	}
	newPath, err = validatePath(newPath)
	if err != nil {
		return err
	}
	if oldPath == "/data" || newPath == "/data" {
		return fmt.Errorf("cannot rename the root data directory")
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	oldRel := strings.TrimPrefix(oldPath, "/data")
	newRel := strings.TrimPrefix(newPath, "/data")
	return s.dispatcher.WorkerFor(gameserverID).RenamePath(ctx, gs.VolumeName, oldRel, newRel)
}

// DownloadFile returns file contents for download — same as ReadFile but named for clarity in handler.
func (s *FileService) DownloadFile(ctx context.Context, gameserverID string, filePath string) ([]byte, error) {
	return s.ReadFile(ctx, gameserverID, filePath)
}

// UploadFile writes an uploaded file to the gameserver volume.
func (s *FileService) UploadFile(ctx context.Context, gameserverID string, filePath string, content []byte) error {
	return s.WriteFile(ctx, gameserverID, filePath, content)
}

func (s *FileService) getGameserver(gameserverID string) (*models.Gameserver, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return gs, nil
}

// validatePath ensures the path is within /data and contains no traversal.
func validatePath(p string) (string, error) {
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/data") {
		return "", fmt.Errorf("path must be within /data, got: %s", p)
	}
	return cleaned, nil
}

