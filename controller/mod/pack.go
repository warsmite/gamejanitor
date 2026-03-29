package mod

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
)

// PackInstallResult is the summary returned after installing a modpack.
type PackInstallResult struct {
	Pack             *model.InstalledMod `json:"pack"`
	ModCount         int                 `json:"mod_count"`
	Overrides        []string            `json:"overrides"`
	Warnings         []string            `json:"warnings,omitempty"`          // non-fatal issues during install
	VersionChanged   string              `json:"version_changed,omitempty"`   // new version if changed
	LoaderChanged    string              `json:"loader_changed,omitempty"`    // new loader if changed
	NeedsRestart     bool                `json:"needs_restart"`               // true if server needs restart/start to apply
	VersionDowngrade bool                `json:"version_downgrade,omitempty"` // true if version went down (world data risk)
}

func (s *ModService) InstallPack(ctx context.Context, gameserverID, sourceName, packID, versionID string) (*PackInstallResult, error) {
	defer s.lockGameserver(gameserverID)()
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	// Check if a modpack is already installed — only one pack allowed at a time.
	// Users should uninstall the existing pack first.
	installed, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("checking existing mods: %w", err)
	}
	for _, m := range installed {
		if m.Delivery == "pack" && m.PackID == nil {
			return nil, controller.ErrConflictf("modpack %q is already installed — uninstall it before installing a new one", m.Name)
		}
	}

	catalog, ok := s.catalogs[sourceName]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", sourceName)
	}

	// Resolve the pack version. Prefer versions with a dedicated server pack file.
	// If a specific versionID is requested, use that. Otherwise pick the latest
	// version that has a server file, falling back to the latest version overall.
	version, err := s.resolvePackVersion(ctx, catalog, packID, versionID)
	if err != nil {
		return nil, err
	}

	// Auto-configure version and loader from the pack's requirements.
	result := &PackInstallResult{NeedsRestart: true}
	envChanged := false

	if version.GameVersion != "" && game.Mods.VersionEnv != "" {
		currentVersion := string(gs.Env[game.Mods.VersionEnv])
		if currentVersion != version.GameVersion {
			// Detect downgrade
			if currentVersion != "" && gs.Installed {
				result.VersionDowngrade = true
			}
			gs.Env[game.Mods.VersionEnv] = version.GameVersion
			result.VersionChanged = version.GameVersion
			envChanged = true
			s.log.Info("pack install: switching game version", "gameserver", gameserverID, "from", currentVersion, "to", version.GameVersion)
		}
	}

	if version.Loader != "" && game.Mods.Loader != nil {
		currentLoader := string(gs.Env[game.Mods.Loader.Env])
		// Find the loader option that matches the pack's required loader
		for optValue := range game.Mods.Loader.Options {
			opt := game.Mods.Loader.Options[optValue]
			if opt.LoaderID == version.Loader || optValue == version.Loader {
				if currentLoader != optValue {
					gs.Env[game.Mods.Loader.Env] = optValue
					result.LoaderChanged = optValue
					envChanged = true
					s.log.Info("pack install: switching loader", "gameserver", gameserverID, "from", currentLoader, "to", optValue)
				}
				break
			}
		}
	}

	// Persist env changes
	if envChanged {
		if err := s.store.UpdateGameserver(gs); err != nil {
			return nil, fmt.Errorf("updating gameserver config for modpack: %w", err)
		}
	}

	// Find the category and source that supports pack delivery
	cat, src := s.findPackSource(game, gs.Env, sourceName)
	if cat == nil || src == nil {
		return nil, controller.ErrBadRequest("pack delivery not available for this game")
	}

	// Download and parse the pack
	contents, err := s.packDel.Install(ctx, gameserverID, version.DownloadURL, src.InstallPath, src.OverridesPath)
	if err != nil {
		return nil, fmt.Errorf("installing modpack: %w", err)
	}

	// Store override paths in metadata so we can clean them up on uninstall
	packMeta := map[string]any{}
	if len(contents.Overrides) > 0 {
		packMeta["overrides"] = contents.Overrides
	}
	metaJSON, _ := json.Marshal(packMeta)

	// Record the modpack itself
	pack := &model.InstalledMod{
		ID:           uuid.New().String(),
		GameserverID: gameserverID,
		Source:       sourceName,
		SourceID:     packID,
		Category:     cat.Name,
		Name:         version.FileName,
		Version:      version.Version,
		VersionID:    version.VersionID,
		DownloadURL:  version.DownloadURL,
		Delivery:     "pack",
		Metadata:     metaJSON,
		InstalledAt:  time.Now(),
	}
	if err := s.store.CreateInstalledMod(pack); err != nil {
		return nil, fmt.Errorf("saving modpack record: %w", err)
	}

	// Record each individual mod, linked to the pack
	var warnings []string
	for _, pm := range contents.Mods {
		existing, _ := s.store.GetInstalledModBySource(gameserverID, sourceName, pm.SourceID)
		if existing != nil {
			s.store.SetModPackID(existing.ID, pack.ID)
			if existing.VersionID != pm.SHA512 {
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
			warnings = append(warnings, fmt.Sprintf("failed to track %s: %v", pm.FileName, err))
		}
	}

	// Track override mods — .jar files extracted from overrides/mods/ that aren't in the files array
	overrideModCount := 0
	for _, overridePath := range contents.Overrides {
		if !strings.HasSuffix(overridePath, ".jar") {
			continue
		}
		fileName := path.Base(overridePath)

		// Skip if already tracked from the files array
		alreadyTracked := false
		for _, pm := range contents.Mods {
			if pm.FileName == fileName {
				alreadyTracked = true
				break
			}
		}
		if alreadyTracked {
			continue
		}

		modRecord := &model.InstalledMod{
			ID:           uuid.New().String(),
			GameserverID: gameserverID,
			Source:       "pack-override",
			SourceID:     overridePath,
			Category:     "Mods",
			Name:         fileName,
			FilePath:     overridePath,
			FileName:     fileName,
			Delivery:     "file",
			PackID:       &pack.ID,
			Metadata:     json.RawMessage(`{}`),
			InstalledAt:  time.Now(),
		}
		if err := s.store.CreateInstalledMod(modRecord); err != nil {
			s.log.Warn("failed to record override mod", "file", fileName, "error", err)
			warnings = append(warnings, fmt.Sprintf("failed to track override %s: %v", fileName, err))
		}
		overrideModCount++
	}

	s.publishEvent(ctx, gameserverID, pack, controller.EventModInstalled)

	result.Pack = pack
	result.ModCount = len(contents.Mods) + overrideModCount
	result.Overrides = contents.Overrides
	result.Warnings = warnings
	return result, nil
}

// resolvePackVersion picks the best version for a server-side modpack install.
// If versionID is specified, uses that exact version. Otherwise prefers the latest
// version that has a dedicated server pack file, falling back to latest overall.
func (s *ModService) resolvePackVersion(ctx context.Context, catalog ModCatalog, packID, versionID string) (*ModVersion, error) {
	if versionID != "" {
		return s.resolveVersion(ctx, catalog, packID, versionID, CatalogFilters{ServerPack: true})
	}

	versions, err := catalog.GetVersions(ctx, packID, CatalogFilters{ServerPack: true})
	if err != nil {
		return nil, fmt.Errorf("getting versions: %w", err)
	}
	if len(versions) == 0 {
		return nil, controller.ErrBadRequest("no versions available for this modpack")
	}

	// Prefer a version with a dedicated server file
	for i := range versions {
		if versions[i].HasServerFile {
			s.log.Info("pack version: using version with server file", "version", versions[i].Version)
			return &versions[i], nil
		}
	}

	// No version has a server file — use the latest (common for Fabric packs)
	return &versions[0], nil
}

func (s *ModService) UpdatePack(ctx context.Context, gameserverID, packModID string) (*PackInstallResult, error) {
	defer s.lockGameserver(gameserverID)()
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

	cat, src := s.findPackSource(game, gs.Env, pack.Source)
	if cat == nil || src == nil {
		return nil, controller.ErrBadRequest("pack delivery not available")
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	// Get latest pack version — prefer server pack file
	filters.ServerPack = true
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
