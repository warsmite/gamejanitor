package mod

import (
	"context"
	"fmt"

	"github.com/warsmite/gamejanitor/games"
)

const maxDepDepth = 10

// installDependencies recursively installs required dependencies for a mod version.
// parentModID is the installed_mod ID of the mod that requires these deps.
func (s *ModService) installDependencies(ctx context.Context, gameserverID, parentModID string, catalog ModCatalog, version *ModVersion, src games.ModCategorySource, category string, filters CatalogFilters, depth int) error {
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

		// Record the dependency (before recursing so its ID exists for grandchildren)
		depMod := s.newInstalledMod(gameserverID, src.Name, dep.ModID, category, depVersion, src.Delivery, true, nil)
		depMod.DependsOn = &parentModID
		if src.Delivery == "file" {
			depMod.FilePath = fmt.Sprintf("%s/%s", src.InstallPath, sanitizeFileName(depVersion.FileName))
			depMod.FileName = sanitizeFileName(depVersion.FileName)
		}

		// Recursive: install the dependency's dependencies first
		if err := s.installDependencies(ctx, gameserverID, depMod.ID, catalog, depVersion, src, category, filters, depth+1); err != nil {
			return err
		}

		// Install the dependency file
		if src.Delivery == "file" {
			if err := s.fileDel.Install(ctx, gameserverID, src.InstallPath, depVersion.DownloadURL, sanitizeFileName(depVersion.FileName)); err != nil {
				return fmt.Errorf("installing dependency %s: %w", dep.ModID, err)
			}
		}

		if err := s.store.CreateInstalledMod(depMod); err != nil {
			s.log.Warn("failed to record auto-installed dependency", "dep_mod_id", dep.ModID, "error", err)
		}
	}
	return nil
}

// removeOrphanedDependencies removes auto-installed mods that were dependencies
// of the removed mod and aren't needed by any other installed mod.
func (s *ModService) removeOrphanedDependencies(ctx context.Context, gameserverID, removedModID string) {
	s.removeOrphanedDepsInner(ctx, gameserverID, removedModID, make(map[string]bool))
}

func (s *ModService) removeOrphanedDepsInner(ctx context.Context, gameserverID, removedModID string, visited map[string]bool) {
	if visited[removedModID] {
		return
	}
	visited[removedModID] = true

	installed, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return
	}

	for _, dep := range installed {
		if !dep.AutoInstalled || dep.DependsOn == nil {
			continue
		}

		// Only consider deps that were installed for the removed mod
		if *dep.DependsOn != removedModID {
			continue
		}

		// Check if any other mod also depends on this one
		stillNeeded := false
		for _, other := range installed {
			if other.ID == removedModID || other.ID == dep.ID {
				continue
			}
			if other.DependsOn != nil && *other.DependsOn == dep.ID {
				stillNeeded = true
				break
			}
		}

		if !stillNeeded {
			// Recursively remove this dep's own orphaned dependencies
			s.removeOrphanedDepsInner(ctx, gameserverID, dep.ID, visited)
			if dep.Delivery == "file" {
				s.fileDel.Uninstall(ctx, gameserverID, dep.FilePath)
			}
			s.store.DeleteInstalledMod(dep.ID)
			s.log.Info("removed orphaned dependency", "mod_id", dep.ID, "name", dep.Name)
		}
	}
}
