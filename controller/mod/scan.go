package mod

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// ScanResult reports the state of mods on disk vs the DB.
type ScanResult struct {
	Tracked   []model.InstalledMod `json:"tracked"`
	Untracked []UntrackedFile      `json:"untracked"`
	Missing   []model.InstalledMod `json:"missing"`
}

// UntrackedFile is a file found on disk in a mod install_path that isn't tracked in the DB.
type UntrackedFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Category string `json:"category"`
}

// Scan cross-references files on disk with the DB for a gameserver.
// Returns tracked mods (DB + disk match), untracked files (on disk but not in DB),
// and missing mods (in DB but not on disk).
func (s *ModService) Scan(ctx context.Context, gameserverID string) (*ScanResult, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver: %w", err)
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	mods, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing installed mods: %w", err)
	}

	// Build set of tracked file paths for quick lookup
	trackedPaths := make(map[string]*model.InstalledMod, len(mods))
	for i := range mods {
		if mods[i].FilePath != "" {
			trackedPaths[mods[i].FilePath] = &mods[i]
		}
	}

	// Use volumeState to check which tracked mods exist on disk
	dirFiles, modsByDir := s.volumeState(ctx, gameserverID, mods)

	var result ScanResult

	// Classify tracked mods as present or missing
	for dir, indices := range modsByDir {
		onDisk := dirFiles[dir]
		for _, i := range indices {
			mod := mods[i]
			if onDisk[mod.FileName] {
				result.Tracked = append(result.Tracked, mod)
			} else {
				result.Missing = append(result.Missing, mod)
			}
		}
	}

	// Also include non-file mods (manifest, pack records) as tracked
	for _, mod := range mods {
		if mod.Delivery != "file" {
			result.Tracked = append(result.Tracked, mod)
		}
	}

	// Scan install_path directories for untracked files
	scannedDirs := make(map[string]bool)
	for _, cat := range game.Mods.Categories {
		installPaths := collectInstallPaths(cat)
		for _, ip := range installPaths {
			if scannedDirs[ip] {
				continue
			}
			scannedDirs[ip] = true

			entries, err := s.fileSvc.ListDirectory(ctx, gameserverID, ip)
			if err != nil {
				continue // directory might not exist yet
			}
			for _, entry := range entries {
				if entry.IsDir {
					continue
				}
				filePath := path.Join(ip, entry.Name)
				if trackedPaths[filePath] != nil {
					continue // already tracked
				}
				result.Untracked = append(result.Untracked, UntrackedFile{
					Name:     entry.Name,
					Path:     filePath,
					Size:     entry.Size,
					Category: cat.Name,
				})
			}
		}
	}

	if result.Tracked == nil {
		result.Tracked = []model.InstalledMod{}
	}
	if result.Untracked == nil {
		result.Untracked = []UntrackedFile{}
	}
	if result.Missing == nil {
		result.Missing = []model.InstalledMod{}
	}
	return &result, nil
}

// TrackFile creates a DB record for an untracked file found on disk.
func (s *ModService) TrackFile(ctx context.Context, gameserverID, category, filePath, name string) (*model.InstalledMod, error) {
	defer s.lockGameserver(gameserverID)()
	if name == "" {
		name = path.Base(filePath)
	}

	// Check it's not already tracked
	existing, _ := s.store.GetInstalledModBySource(gameserverID, "detected", filePath)
	if existing != nil {
		return existing, nil
	}

	mod := &model.InstalledMod{
		ID:           uuid.New().String(),
		GameserverID: gameserverID,
		Source:       "detected",
		SourceID:     filePath,
		Category:     category,
		Name:         name,
		FilePath:     filePath,
		FileName:     path.Base(filePath),
		Delivery:     "file",
		Metadata:     json.RawMessage(`{}`),
		InstalledAt:  time.Now(),
	}

	if err := s.store.CreateInstalledMod(mod); err != nil {
		return nil, fmt.Errorf("tracking file: %w", err)
	}

	s.publishEvent(ctx, gameserverID, mod, event.EventModInstalled)
	return mod, nil
}

// collectInstallPaths returns all unique install paths for a category
// (from category-level and/or per-source).
func collectInstallPaths(cat games.ModCategoryDef) []string {
	seen := make(map[string]bool)
	var paths []string

	if cat.InstallPath != "" {
		seen[cat.InstallPath] = true
		paths = append(paths, cat.InstallPath)
	}
	for _, src := range cat.Sources {
		ip := cat.ResolveInstallPath(&src)
		if ip != "" && !seen[ip] {
			seen[ip] = true
			paths = append(paths, ip)
		}
	}
	return paths
}
