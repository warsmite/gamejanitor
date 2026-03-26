package gameserver

import (
	"context"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker"
)

// FileStore abstracts the database operations the file service needs.
type FileStore interface {
	GetGameserver(id string) (*model.Gameserver, error)
}

type FileService struct {
	store      FileStore
	dispatcher *orchestrator.Dispatcher
	log        *slog.Logger
}

func NewFileService(store FileStore, dispatcher *orchestrator.Dispatcher, log *slog.Logger) *FileService {
	return &FileService{
		store:      store,
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

	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.ListFiles(ctx, gs.VolumeName, relPath)
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
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.ReadFile(ctx, gs.VolumeName, relPath)
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

	relPath := strings.TrimPrefix(filePath, "/data")
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.WriteFile(ctx, gs.VolumeName, relPath, content, 0644)
}

func (s *FileService) DeletePath(ctx context.Context, gameserverID string, targetPath string) error {
	targetPath, err := validatePath(targetPath)
	if err != nil {
		return err
	}
	if targetPath == "/data" {
		return controller.ErrBadRequest("cannot delete the root data directory")
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	relPath := strings.TrimPrefix(targetPath, "/data")
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.DeletePath(ctx, gs.VolumeName, relPath)
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
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.CreateDirectory(ctx, gs.VolumeName, relPath)
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
		return controller.ErrBadRequest("cannot rename the root data directory")
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return err
	}

	oldRel := strings.TrimPrefix(oldPath, "/data")
	newRel := strings.TrimPrefix(newPath, "/data")
	w := s.dispatcher.WorkerFor(gameserverID)
	if w == nil {
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", gameserverID)
	}
	return w.RenamePath(ctx, gs.VolumeName, oldRel, newRel)
}

// DownloadFile returns file contents for download — same as ReadFile but named for clarity in handler.
func (s *FileService) DownloadFile(ctx context.Context, gameserverID string, filePath string) ([]byte, error) {
	return s.ReadFile(ctx, gameserverID, filePath)
}

// UploadFile writes an uploaded file to the gameserver volume.
func (s *FileService) UploadFile(ctx context.Context, gameserverID string, filePath string, content []byte) error {
	return s.WriteFile(ctx, gameserverID, filePath, content)
}

func (s *FileService) getGameserver(gameserverID string) (*model.Gameserver, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return gs, nil
}

// validatePath ensures the path is within /data and contains no traversal.
func validatePath(p string) (string, error) {
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/data") {
		return "", controller.ErrBadRequestf("path must be within /data, got: %s", p)
	}
	return cleaned, nil
}
