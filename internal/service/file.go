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

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

const fileopsImage = "alpine:latest"

type FileService struct {
	db     *sql.DB
	docker *docker.Client
	log    *slog.Logger
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
		db:     db,
		docker: dockerClient,
		log:    log,
	}
}

func fileopsContainerName(gameserverID string) string {
	return "gamejanitor-fileops-" + gameserverID
}

// ensureFileopsContainer guarantees a running fileops container exists for the
// given gameserver, creating or recovering it as needed.
func (s *FileService) ensureFileopsContainer(ctx context.Context, gs *models.Gameserver) (string, error) {
	containerName := fileopsContainerName(gs.ID)

	info, err := s.docker.InspectContainer(ctx, containerName)
	if err == nil {
		if info.State == "running" {
			return info.ID, nil
		}
		// Exists but not running — try to start it
		if startErr := s.docker.StartContainer(ctx, info.ID); startErr == nil {
			s.log.Info("restarted fileops container", "gameserver_id", gs.ID)
			return info.ID, nil
		}
		// Can't start — remove and recreate
		s.log.Warn("removing unrecoverable fileops container", "gameserver_id", gs.ID, "state", info.State)
		s.docker.RemoveContainer(ctx, info.ID)
	}

	containerID, err := s.docker.CreateContainer(ctx, docker.ContainerOptions{
		Name:       containerName,
		Image:      fileopsImage,
		Env:        []string{},
		VolumeName: gs.VolumeName,
		Entrypoint: []string{"sleep", "infinity"},
		User:       "1001:1001",
	})
	if err != nil {
		return "", fmt.Errorf("creating fileops container for %s: %w", gs.ID, err)
	}

	if err := s.docker.StartContainer(ctx, containerID); err != nil {
		s.docker.RemoveContainer(ctx, containerID)
		return "", fmt.Errorf("starting fileops container for %s: %w", gs.ID, err)
	}

	s.log.Info("created fileops container", "gameserver_id", gs.ID)
	return containerID, nil
}

// RemoveFileopsContainer removes the fileops container for a gameserver.
// Called when a gameserver is deleted.
func (s *FileService) RemoveFileopsContainer(ctx context.Context, gameserverID string) {
	containerName := fileopsContainerName(gameserverID)
	if err := s.docker.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no fileops container to remove", "gameserver_id", gameserverID)
	} else {
		s.log.Info("removed fileops container", "gameserver_id", gameserverID)
	}
}

// RecoverOnStartup ensures all existing gameservers have running fileops containers.
// Handles migration from the old temp container system and recovery after crashes.
func (s *FileService) RecoverOnStartup(ctx context.Context) error {
	s.log.Info("pulling fileops image", "image", fileopsImage)
	if err := s.docker.PullImage(ctx, fileopsImage); err != nil {
		return fmt.Errorf("pulling fileops image: %w", err)
	}

	gameservers, err := models.ListGameservers(s.db, models.GameserverFilter{})
	if err != nil {
		return fmt.Errorf("listing gameservers for fileops recovery: %w", err)
	}

	for _, gs := range gameservers {
		if _, err := s.ensureFileopsContainer(ctx, &gs); err != nil {
			s.log.Warn("failed to ensure fileops container on startup", "gameserver_id", gs.ID, "error", err)
		}
	}

	s.log.Info("fileops containers recovered", "count", len(gameservers))
	return nil
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
			return fmt.Errorf("listing directory %s: %s", dirPath, stderr)
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
			return fmt.Errorf("deleting %s: %s", targetPath, stderr)
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
			return fmt.Errorf("creating directory %s: %s", dirPath, stderr)
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
		return ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	containerID, err := s.ensureFileopsContainer(ctx, gs)
	if err != nil {
		return err
	}
	return fn(containerID)
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
