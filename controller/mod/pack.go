package mod

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

// PackInstallResult is the summary returned after installing a modpack.
type PackInstallResult struct {
	Pack      *model.InstalledMod `json:"pack"`
	ModCount  int                 `json:"mod_count"`
	Overrides []string            `json:"overrides"`
}

func (s *ModService) InstallPack(ctx context.Context, gameserverID, sourceName, packID, versionID string) (*PackInstallResult, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, "Modpacks")
	if cat == nil {
		return nil, controller.ErrBadRequest("modpacks not available for this game")
	}
	src := findSource(cat, sourceName)
	if src == nil {
		return nil, controller.ErrBadRequestf("source %q not available for modpacks", sourceName)
	}
	if src.Delivery != "pack" {
		return nil, controller.ErrBadRequest("source does not support pack delivery")
	}

	catalog, ok := s.catalogs[sourceName]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", sourceName)
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	// Resolve pack version
	version, err := s.resolveVersion(ctx, catalog, packID, versionID, filters)
	if err != nil {
		return nil, err
	}

	// Download and parse the pack
	contents, err := s.packDel.Install(ctx, gameserverID, version.DownloadURL, src.InstallPath, src.OverridesPath)
	if err != nil {
		return nil, fmt.Errorf("installing modpack: %w", err)
	}

	// Record the modpack itself — store the .mrpack download URL for reconciliation
	pack := &model.InstalledMod{
		ID:           uuid.New().String(),
		GameserverID: gameserverID,
		Source:       sourceName,
		SourceID:     packID,
		Category:     "Modpacks",
		Name:         version.FileName,
		Version:      version.Version,
		VersionID:    version.VersionID,
		DownloadURL:  version.DownloadURL,
		Delivery:     "pack",
		Metadata:     json.RawMessage(`{}`),
		InstalledAt:  time.Now(),
	}
	if err := s.store.CreateInstalledMod(pack); err != nil {
		return nil, fmt.Errorf("saving modpack record: %w", err)
	}

	// Record each individual mod, linked to the pack
	for _, pm := range contents.Mods {
		existing, _ := s.store.GetInstalledModBySource(gameserverID, sourceName, pm.SourceID)
		if existing != nil {
			// Mod already installed — link to pack, update version if different
			s.store.SetModPackID(existing.ID, pack.ID)
			if existing.VersionID != pm.SHA512 { // use hash as version proxy for pack mods
				s.store.UpdateModVersion(existing.ID, pm.SHA512, "")
			}
			continue
		}

		modRecord := &model.InstalledMod{
			ID:           uuid.New().String(),
			GameserverID: gameserverID,
			Source:       sourceName,
			SourceID:     pm.SourceID,
			Category:     "Mods",
			Name:         pm.FileName,
			Version:      "",
			VersionID:    pm.SHA512,
			FilePath:     pm.FilePath,
			FileName:     pm.FileName,
			DownloadURL:  pm.DownloadURL,
			Delivery:     "file",
			PackID:       &pack.ID,
			Metadata:     json.RawMessage(`{}`),
			InstalledAt:  time.Now(),
		}
		if err := s.store.CreateInstalledMod(modRecord); err != nil {
			s.log.Warn("failed to record pack mod", "file", pm.FileName, "error", err)
		}
	}

	s.publishEvent(ctx, gameserverID, pack, controller.EventModInstalled)

	return &PackInstallResult{
		Pack:      pack,
		ModCount:  len(contents.Mods),
		Overrides: contents.Overrides,
	}, nil
}

func (s *ModService) UpdatePack(ctx context.Context, gameserverID, packModID string) (*PackInstallResult, error) {
	pack, err := s.store.GetInstalledMod(packModID)
	if err != nil || pack == nil {
		return nil, controller.ErrNotFound("modpack not found")
	}
	if pack.GameserverID != gameserverID || pack.Delivery != "pack" {
		return nil, controller.ErrNotFound("modpack not found")
	}

	catalog, ok := s.catalogs[pack.Source]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", pack.Source)
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, "Modpacks")
	if cat == nil {
		return nil, controller.ErrBadRequest("modpacks not available")
	}
	src := findSource(cat, pack.Source)
	if src == nil {
		return nil, controller.ErrBadRequest("source not available")
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	// Get latest pack version
	versions, err := catalog.GetVersions(ctx, pack.SourceID, filters)
	if err != nil || len(versions) == 0 {
		return nil, fmt.Errorf("no versions available for modpack")
	}
	latest := versions[0]
	if latest.VersionID == pack.VersionID {
		return &PackInstallResult{Pack: pack, ModCount: 0}, nil // already up to date
	}

	// Download and parse new pack
	newContents, err := s.packDel.Install(ctx, gameserverID, latest.DownloadURL, src.InstallPath, src.OverridesPath)
	if err != nil {
		return nil, fmt.Errorf("downloading updated modpack: %w", err)
	}

	// Load current pack mods and exclusions
	currentMods, _ := s.store.ListModsByPackID(gameserverID, packModID)
	exclusions, _ := s.store.GetPackExclusions(packModID)

	// Build lookup of new pack's mods by filename (source ID might not be available for pack mods)
	newModSet := make(map[string]*PackMod)
	for i := range newContents.Mods {
		newModSet[newContents.Mods[i].FileName] = &newContents.Mods[i]
	}

	// Remove mods dropped from the pack
	for _, current := range currentMods {
		if _, inNew := newModSet[current.FileName]; !inNew {
			s.fileDel.Uninstall(ctx, gameserverID, current.FilePath)
			s.store.DeleteInstalledMod(current.ID)
		}
	}

	// Add new mods and update changed versions (skip excluded)
	for _, newMod := range newContents.Mods {
		if exclusions[newMod.SourceID] || exclusions[newMod.FileName] {
			continue
		}

		// Check if already installed
		var found *model.InstalledMod
		for i := range currentMods {
			if currentMods[i].FileName == newMod.FileName {
				found = &currentMods[i]
				break
			}
		}

		if found != nil && found.VersionID == newMod.SHA512 {
			continue // same version
		}

		if found != nil {
			// Version changed — update
			s.fileDel.Uninstall(ctx, gameserverID, found.FilePath)
			s.store.DeleteInstalledMod(found.ID)
		}

		// Record new/updated mod
		modRecord := &model.InstalledMod{
			ID:           uuid.New().String(),
			GameserverID: gameserverID,
			Source:       pack.Source,
			SourceID:     newMod.SourceID,
			Category:     "Mods",
			Name:         newMod.FileName,
			VersionID:    newMod.SHA512,
			FilePath:     newMod.FilePath,
			FileName:     newMod.FileName,
			DownloadURL:  newMod.DownloadURL,
			Delivery:     "file",
			PackID:       &pack.ID,
			Metadata:     json.RawMessage(`{}`),
			InstalledAt:  time.Now(),
		}
		s.store.CreateInstalledMod(modRecord)
	}

	// Update pack record
	s.store.UpdateModVersion(packModID, latest.VersionID, latest.Version)

	return &PackInstallResult{
		Pack:      pack,
		ModCount:  len(newContents.Mods),
		Overrides: newContents.Overrides,
	}, nil
}
