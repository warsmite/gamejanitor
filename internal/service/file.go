package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

const tempContainerIdleTimeout = 5 * time.Minute

type tempFileContainer struct {
	containerID string
	timer       *time.Timer
}

type FileService struct {
	db             *sql.DB
	docker         *docker.Client
	log            *slog.Logger
	mu             sync.Mutex
	tempContainers map[string]*tempFileContainer
}

type FileEntry struct {
	Name        string
	IsDir       bool
	Size        int64
	ModTime     string
	Permissions string
}

func NewFileService(db *sql.DB, dockerClient *docker.Client, log *slog.Logger) *FileService {
	return &FileService{
		db:             db,
		docker:         dockerClient,
		log:            log,
		tempContainers: make(map[string]*tempFileContainer),
	}
}

func (s *FileService) ListDirectory(ctx context.Context, gameserverID string, dirPath string) ([]FileEntry, error) {
	dirPath, err := validatePath(dirPath)
	if err != nil {
		return nil, err
	}

	var entries []FileEntry
	err = s.withContainer(ctx, gameserverID, func(containerID string) error {
		exitCode, stdout, stderr, execErr := s.docker.Exec(ctx, containerID, []string{"ls", "-la", dirPath})
		if execErr != nil {
			return fmt.Errorf("listing directory %s: %w", dirPath, execErr)
		}
		if exitCode != 0 {
			return fmt.Errorf("listing directory %s failed: %s", dirPath, stderr)
		}
		entries = parseLsOutput(stdout)
		return nil
	})
	return entries, err
}

func (s *FileService) ReadFile(ctx context.Context, gameserverID string, filePath string) ([]byte, error) {
	filePath, err := validatePath(filePath)
	if err != nil {
		return nil, err
	}

	var content []byte
	err = s.withContainer(ctx, gameserverID, func(containerID string) error {
		var copyErr error
		content, copyErr = s.docker.CopyFromContainer(ctx, containerID, filePath)
		return copyErr
	})
	return content, err
}

func (s *FileService) WriteFile(ctx context.Context, gameserverID string, filePath string, content []byte) error {
	filePath, err := validatePath(filePath)
	if err != nil {
		return err
	}

	return s.withContainer(ctx, gameserverID, func(containerID string) error {
		return s.docker.CopyToContainer(ctx, containerID, filePath, content)
	})
}

func (s *FileService) DeletePath(ctx context.Context, gameserverID string, targetPath string) error {
	targetPath, err := validatePath(targetPath)
	if err != nil {
		return err
	}
	if targetPath == "/data" {
		return fmt.Errorf("cannot delete the root data directory")
	}

	return s.withContainer(ctx, gameserverID, func(containerID string) error {
		exitCode, _, stderr, execErr := s.docker.Exec(ctx, containerID, []string{"rm", "-rf", targetPath})
		if execErr != nil {
			return fmt.Errorf("deleting %s: %w", targetPath, execErr)
		}
		if exitCode != 0 {
			return fmt.Errorf("deleting %s failed: %s", targetPath, stderr)
		}
		return nil
	})
}

func (s *FileService) CreateDirectory(ctx context.Context, gameserverID string, dirPath string) error {
	dirPath, err := validatePath(dirPath)
	if err != nil {
		return err
	}

	return s.withContainer(ctx, gameserverID, func(containerID string) error {
		exitCode, _, stderr, execErr := s.docker.Exec(ctx, containerID, []string{"mkdir", "-p", dirPath})
		if execErr != nil {
			return fmt.Errorf("creating directory %s: %w", dirPath, execErr)
		}
		if exitCode != 0 {
			return fmt.Errorf("creating directory %s failed: %s", dirPath, stderr)
		}
		return nil
	})
}

func (s *FileService) withContainer(ctx context.Context, gameserverID string, fn func(containerID string) error) error {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return fmt.Errorf("getting gameserver %s: %w", gameserverID, err)
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found", gameserverID)
	}

	if isRunningStatus(gs.Status) && gs.ContainerID != nil {
		s.log.Debug("file operation on running container", "gameserver_id", gameserverID)
		return fn(*gs.ContainerID)
	}

	if gs.Status == StatusStopped || gs.Status == StatusError {
		containerID, err := s.getTempContainer(ctx, gs)
		if err != nil {
			return fmt.Errorf("getting temp container for file access: %w", err)
		}
		s.log.Debug("file operation on temp container", "gameserver_id", gameserverID)
		return fn(containerID)
	}

	return fmt.Errorf("cannot access files while gameserver is %s", gs.Status)
}

func (s *FileService) getTempContainer(ctx context.Context, gs *models.Gameserver) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tc, ok := s.tempContainers[gs.ID]; ok {
		tc.timer.Reset(tempContainerIdleTimeout)
		s.log.Debug("reusing temp file container", "gameserver_id", gs.ID)
		return tc.containerID, nil
	}

	game, err := models.GetGame(s.db, gs.GameID)
	if err != nil {
		return "", fmt.Errorf("getting game %s: %w", gs.GameID, err)
	}
	if game == nil {
		return "", fmt.Errorf("game %s not found", gs.GameID)
	}

	containerName := "gamejanitor-files-" + gs.ID
	containerID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       containerName,
		Image:      game.Image,
		Env:        []string{},
		VolumeName: gs.VolumeName,
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return "", fmt.Errorf("creating temp file container: %w", err)
	}

	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		s.docker.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("starting temp file container: %w", err)
	}

	timer := time.AfterFunc(tempContainerIdleTimeout, func() {
		s.cleanupTempContainer(gs.ID)
	})

	s.tempContainers[gs.ID] = &tempFileContainer{
		containerID: containerID,
		timer:       timer,
	}

	s.log.Info("created temp file container", "gameserver_id", gs.ID, "container_id", containerID[:12])
	return containerID, nil
}

// CleanupTempContainer removes a temp file container for the given gameserver.
// Called before starting a gameserver to release the volume.
func (s *FileService) CleanupTempContainer(gameserverID string) {
	s.cleanupTempContainer(gameserverID)
}

func (s *FileService) cleanupTempContainer(gameserverID string) {
	s.mu.Lock()
	tc, ok := s.tempContainers[gameserverID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.tempContainers, gameserverID)
	s.mu.Unlock()

	tc.timer.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := s.docker.StopContainer(ctx, tc.containerID, 5); err != nil {
		s.log.Warn("failed to stop temp file container", "gameserver_id", gameserverID, "error", err)
	}
	if err := s.docker.RemoveContainer(ctx, tc.containerID); err != nil {
		s.log.Warn("failed to remove temp file container", "gameserver_id", gameserverID, "error", err)
	}

	s.log.Info("cleaned up temp file container", "gameserver_id", gameserverID)
}

// CleanupAll removes all temp file containers. Called on shutdown.
func (s *FileService) CleanupAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.tempContainers))
	for id := range s.tempContainers {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, id := range ids {
		s.cleanupTempContainer(id)
	}
	s.log.Info("cleaned up all temp file containers", "count", len(ids))
}

// validatePath ensures the path is within /data and contains no traversal.
func validatePath(p string) (string, error) {
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/data") {
		return "", fmt.Errorf("path must be within /data, got: %s", p)
	}
	return cleaned, nil
}

// parseLsOutput parses `ls -la` output into FileEntry structs.
func parseLsOutput(output string) []FileEntry {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var entries []FileEntry

	for _, line := range lines {
		if strings.HasPrefix(line, "total ") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}

		name := strings.Join(fields[8:], " ")
		// Handle symlinks: "name -> target"
		if idx := strings.Index(name, " -> "); idx >= 0 {
			name = name[:idx]
		}

		// Skip . and .. entries
		if name == "." || name == ".." {
			continue
		}

		perms := fields[0]
		isDir := len(perms) > 0 && perms[0] == 'd'
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		modTime := fields[5] + " " + fields[6] + " " + fields[7]

		entries = append(entries, FileEntry{
			Name:        name,
			IsDir:       isDir,
			Size:        size,
			ModTime:     modTime,
			Permissions: perms,
		})
	}

	// Sort: directories first, then alphabetical by name (case-insensitive)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	return entries
}
