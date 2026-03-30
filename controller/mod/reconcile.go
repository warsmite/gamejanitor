package mod

import (
	"context"
	"fmt"
	"path"

	"github.com/warsmite/gamejanitor/model"
)

// volumeState cross-references DB-tracked file-delivery mods against what's actually on disk.
// Groups mods by parent directory and batches ListDirectory calls for efficiency.
// Returns: filesOnDisk (dir → filename set), modsByDir (dir → mod indices into the slice).
func (s *ModService) volumeState(ctx context.Context, gameserverID string, mods []model.InstalledMod) (map[string]map[string]bool, map[string][]int) {
	dirFiles := make(map[string]map[string]bool) // dir → set of filenames on disk
	modsByDir := make(map[string][]int)           // dir → indices into mods slice

	for i, mod := range mods {
		if mod.Delivery != "file" || mod.FilePath == "" {
			continue
		}
		dir := path.Dir(mod.FilePath)
		modsByDir[dir] = append(modsByDir[dir], i)

		if _, listed := dirFiles[dir]; !listed {
			entries, err := s.fileSvc.ListDirectory(ctx, gameserverID, dir)
			if err != nil {
				s.log.Debug("volumeState: cannot list directory", "dir", dir, "error", err)
				dirFiles[dir] = make(map[string]bool)
				continue
			}
			names := make(map[string]bool, len(entries))
			for _, e := range entries {
				if !e.IsDir {
					names[e.Name] = true
				}
			}
			dirFiles[dir] = names
		}
	}
	return dirFiles, modsByDir
}

// Reconcile ensures all DB-tracked mods exist on the gameserver volume.
// Called automatically before container start as a safety net.
// Downloads missing files, regenerates manifests. Non-fatal — logs warnings for
// unrecoverable mods (uploads, detected files) but never blocks server start.
func (s *ModService) Reconcile(ctx context.Context, gameserverID string) error {
	defer s.lockGameserver(gameserverID)()
	mods, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return fmt.Errorf("listing installed mods: %w", err)
	}
	if len(mods) == 0 {
		return nil
	}

	dirFiles, modsByDir := s.volumeState(ctx, gameserverID, mods)

	// Check each file-delivery mod
	var recovered, failed int
	for dir, indices := range modsByDir {
		onDisk := dirFiles[dir]
		for _, i := range indices {
			mod := mods[i]
			if onDisk[mod.FileName] {
				continue // file exists, skip
			}

			// File missing — try to recover
			s.log.Info("reconcile: missing mod file, attempting recovery", "mod", mod.Name, "path", mod.FilePath)

			downloadURL := mod.DownloadURL
			if downloadURL == "" {
				// Try to resolve from catalog
				downloadURL = s.resolveDownloadURL(ctx, mod)
			}

			if downloadURL == "" {
				s.log.Warn("reconcile: cannot recover mod, no download URL", "mod", mod.Name, "source", mod.Source, "id", mod.ID)
				failed++
				continue
			}

			if err := s.fileSvc.DownloadToVolume(ctx, gameserverID, downloadURL, mod.FilePath, "", 0); err != nil {
				s.log.Warn("reconcile: failed to re-download mod", "mod", mod.Name, "url", downloadURL, "error", err)
				failed++
				continue
			}

			// Cache the URL for next time if it wasn't stored
			if mod.DownloadURL == "" {
				s.store.UpdateModDownloadURL(mod.ID, downloadURL)
			}
			recovered++
		}
	}

	// Handle manifest-delivery mods — regenerate manifest files
	if err := s.reconcileManifests(ctx, gameserverID, mods); err != nil {
		s.log.Warn("reconcile: manifest regeneration had errors", "error", err)
	}

	// Handle pack records — if any child mods were recovered, re-extract pack overrides
	if recovered > 0 {
		s.reconcilePackOverrides(ctx, gameserverID, mods)
	}

	if recovered > 0 || failed > 0 {
		s.log.Info("reconcile: complete", "gameserver", gameserverID, "recovered", recovered, "failed", failed)
	}
	return nil
}

// resolveDownloadURL queries the catalog for the current download URL of an installed mod.
// Used when the mod was installed before download_url persistence was added.
func (s *ModService) resolveDownloadURL(ctx context.Context, mod model.InstalledMod) string {
	catalog, ok := s.catalogs[mod.Source]
	if !ok {
		return ""
	}

	// Build minimal filters — we just need to find the specific version
	versions, err := catalog.GetVersions(ctx, mod.SourceID, CatalogFilters{})
	if err != nil || len(versions) == 0 {
		return ""
	}

	// Find the specific version we have installed
	for _, v := range versions {
		if v.VersionID == mod.VersionID {
			return v.DownloadURL
		}
	}

	// Fallback: return latest version URL (better than nothing)
	return versions[0].DownloadURL
}

// reconcileManifests regenerates manifest files from DB state.
// For Workshop-style mods, the manifest is the source of truth for mod downloads.
func (s *ModService) reconcileManifests(ctx context.Context, gameserverID string, mods []model.InstalledMod) error {
	// Group manifest mods by source to find their manifest paths
	manifestSources := make(map[string][]string) // source → mod source_ids
	for _, mod := range mods {
		if mod.Delivery == "manifest" {
			manifestSources[mod.Source] = append(manifestSources[mod.Source], mod.SourceID)
		}
	}

	if len(manifestSources) == 0 {
		return nil
	}

	// Look up the game definition to find manifest paths
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil
	}

	for _, cat := range game.Mods.Categories {
		for _, src := range cat.Sources {
			if src.Delivery != "manifest" {
				continue
			}
			ids, ok := manifestSources[src.Name]
			if !ok || len(ids) == 0 {
				continue
			}
			manifestPath := src.Config["manifest_path"]
			if manifestPath == "" {
				continue
			}
			if err := s.manifestDel.Install(ctx, gameserverID, manifestPath, ids); err != nil {
				s.log.Warn("reconcile: failed to regenerate manifest", "source", src.Name, "error", err)
			}
		}
	}
	return nil
}

// reconcilePackOverrides re-downloads pack .mrpack files and re-extracts overrides
// when child mods were recovered (indicating the volume was wiped).
func (s *ModService) reconcilePackOverrides(ctx context.Context, gameserverID string, mods []model.InstalledMod) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return
	}

	for _, mod := range mods {
		if mod.Delivery != "pack" || mod.DownloadURL == "" {
			continue
		}

		// Find the pack's overrides path from the game YAML
		var overridesPath string
		for _, cat := range game.Mods.Categories {
			for _, src := range cat.Sources {
				if src.Delivery == "pack" {
					overridesPath = src.OverridesPath
					break
				}
			}
			if overridesPath != "" {
				break
			}
		}
		if overridesPath == "" {
			continue
		}

		// Re-download and re-extract overrides only (mods are already recovered individually)
		s.log.Info("reconcile: re-extracting pack overrides", "pack", mod.Name)
		// The packDel.Install re-downloads all mods too, but they already exist on disk
		// so the worker's DownloadFile will overwrite with the same content. The important
		// part is the override extraction.
		installPath := ""
		for _, cat := range game.Mods.Categories {
			for _, src := range cat.Sources {
				if src.Delivery == "pack" {
					installPath = cat.ResolveInstallPath(&src)
					break
				}
			}
			if installPath != "" {
				break
			}
		}
		if installPath == "" {
			continue
		}

		if _, err := s.packDel.Install(ctx, gameserverID, mod.DownloadURL, installPath, overridesPath); err != nil {
			s.log.Warn("reconcile: failed to re-extract pack overrides", "pack", mod.Name, "error", err)
		}
	}
}
