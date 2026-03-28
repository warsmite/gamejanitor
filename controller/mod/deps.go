package mod

import (
	"context"
	"fmt"

	"github.com/warsmite/gamejanitor/games"
)

const maxDepDepth = 10

func (s *ModService) installDependencies(ctx context.Context, gameserverID string, catalog ModCatalog, version *ModVersion, src games.ModCategorySource, category string, filters CatalogFilters, depth int) error {
	if depth >= maxDepDepth {
		return fmt.Errorf("dependency depth limit reached (%d levels)", maxDepDepth)
	}
	if depth >= 5 {
		s.log.Warn("deep dependency chain", "depth", depth, "version", version.VersionID)
	}

	deps, err := catalog.GetDependencies(ctx, version.VersionID)
	if err != nil || len(deps) == 0 {
		return nil
	}

	for _, dep := range deps {
		if !dep.Required {
			continue
		}

		// Already installed?
		existing, _ := s.store.GetInstalledModBySource(gameserverID, src.Name, dep.ModID)
		if existing != nil {
			continue
		}

		// Resolve the dependency's best version
		depVersion, err := s.resolveVersion(ctx, catalog, dep.ModID, dep.VersionID, filters)
		if err != nil {
			s.log.Warn("failed to resolve dependency, skipping", "dep_mod_id", dep.ModID, "error", err)
			continue
		}

		// Recursive: install the dependency's dependencies first
		if err := s.installDependencies(ctx, gameserverID, catalog, depVersion, src, category, filters, depth+1); err != nil {
			return err
		}

		// Install the dependency itself
		if src.Delivery == "file" {
			if err := s.fileDel.Install(ctx, gameserverID, src.InstallPath, depVersion.DownloadURL, sanitizeFileName(depVersion.FileName)); err != nil {
				return fmt.Errorf("installing dependency %s: %w", dep.ModID, err)
			}
		}

		// Record as auto-installed
		depMod := s.newInstalledMod(gameserverID, src.Name, dep.ModID, category, depVersion, src.Delivery, true, nil)
		if src.Delivery == "file" {
			depMod.FilePath = fmt.Sprintf("%s/%s", src.InstallPath, sanitizeFileName(depVersion.FileName))
			depMod.FileName = sanitizeFileName(depVersion.FileName)
		}
		dependsOn := version.VersionID // track what required this dep
		depMod.DependsOn = &dependsOn

		if err := s.store.CreateInstalledMod(depMod); err != nil {
			s.log.Warn("failed to record auto-installed dependency", "dep_mod_id", dep.ModID, "error", err)
		}
	}
	return nil
}

func (s *ModService) removeOrphanedDependencies(ctx context.Context, gameserverID, removedModID string) {
	installed, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return
	}

	for _, dep := range installed {
		if !dep.AutoInstalled || dep.DependsOn == nil {
			continue
		}

		// Check if this dep was required by the removed mod
		// (DependsOn stores the version ID of the parent, not the mod ID,
		// but for orphan cleanup we check if any non-removed mod still needs it)
		stillNeeded := false
		for _, other := range installed {
			if other.ID == removedModID || other.ID == dep.ID {
				continue
			}
			// A simple heuristic: if another mod of the same source exists,
			// keep the dependency (it might need it too)
			if other.Source == dep.Source && !other.AutoInstalled {
				stillNeeded = true
				break
			}
		}

		if !stillNeeded {
			if dep.Delivery == "file" {
				s.fileDel.Uninstall(ctx, gameserverID, dep.FilePath)
			}
			s.store.DeleteInstalledMod(dep.ID)
			s.log.Info("removed orphaned dependency", "mod_id", dep.ID, "name", dep.Name)
		}
	}
}
