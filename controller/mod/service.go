package mod

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// Store abstracts DB operations needed by the mod service.
type Store interface {
	ListInstalledMods(gameserverID string) ([]model.InstalledMod, error)
	GetInstalledMod(id string) (*model.InstalledMod, error)
	GetInstalledModBySource(gameserverID, source, sourceID string) (*model.InstalledMod, error)
	CreateInstalledMod(m *model.InstalledMod) error
	DeleteInstalledMod(id string) error
	GetGameserver(id string) (*model.Gameserver, error)
	ListModsByPackID(gameserverID, packID string) ([]model.InstalledMod, error)
	GetPackExclusions(packModID string) (map[string]bool, error)
	CreatePackExclusion(e *model.PackExclusion) error
	SetModPackID(modID, packID string) error
	UpdateModVersion(modID, versionID, version string) error
}

// FileOperator is a narrow interface for file operations the mod service needs.
type FileOperator interface {
	WriteFile(ctx context.Context, gameserverID string, filePath string, content []byte) error
	DeletePath(ctx context.Context, gameserverID string, targetPath string) error
	CreateDirectory(ctx context.Context, gameserverID string, dirPath string) error
}

type ModService struct {
	catalogs    map[string]ModCatalog
	fileDel     *FileDelivery
	manifestDel *ManifestDelivery
	packDel     *PackDelivery
	store       Store
	gameStore   *games.GameStore
	options     *games.OptionsRegistry
	broadcaster *controller.EventBus
	log         *slog.Logger
}

func NewModService(store Store, fileSvc FileOperator, gameStore *games.GameStore, options *games.OptionsRegistry, broadcaster *controller.EventBus, log *slog.Logger) *ModService {
	return &ModService{
		catalogs:    make(map[string]ModCatalog),
		fileDel:     NewFileDelivery(fileSvc, log),
		manifestDel: NewManifestDelivery(fileSvc, log),
		packDel:     NewPackDelivery(fileSvc, log),
		store:       store,
		gameStore:   gameStore,
		options:     options,
		broadcaster: broadcaster,
		log:         log,
	}
}

// RegisterCatalog adds a mod catalog (source) to the service.
func (s *ModService) RegisterCatalog(name string, catalog ModCatalog) {
	s.catalogs[name] = catalog
}

// --- Query methods ---

// ModTabConfig is everything the mods tab needs in one call.
type ModTabConfig struct {
	Version    *VersionPickerConfig `json:"version,omitempty"`
	Loader     *LoaderPickerConfig  `json:"loader,omitempty"`
	Categories []games.ModCategoryDef `json:"categories"`
}

type VersionPickerConfig struct {
	Env     string         `json:"env"`
	Current string         `json:"current"`
	Options []games.Option `json:"options"`
}

type LoaderPickerConfig struct {
	Env     string   `json:"env"`
	Current string   `json:"current"`
	Options []string `json:"options"`
}

// GetConfig returns the full mods tab configuration: version picker, loader picker,
// and available categories. One call gives the UI everything it needs.
func (s *ModService) GetConfig(ctx context.Context, gameserverID string) (*ModTabConfig, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	config := &ModTabConfig{
		Categories: s.availableCategories(game, gs.Env),
	}

	// Version picker
	if game.Mods.VersionEnv != "" {
		vc := &VersionPickerConfig{
			Env:     game.Mods.VersionEnv,
			Current: string(gs.Env[game.Mods.VersionEnv]),
		}
		// Resolve dynamic options for the version env var
		for _, e := range game.DefaultEnv {
			if e.Key == game.Mods.VersionEnv && e.DynamicOptions != nil && s.options != nil {
				opts, err := s.options.GetOptionsForEnv(e)
				if err == nil {
					vc.Options = opts
				}
				break
			}
		}
		config.Version = vc
	}

	// Loader picker
	if game.Mods.Loader != nil {
		lc := &LoaderPickerConfig{
			Env:     game.Mods.Loader.Env,
			Current: string(gs.Env[game.Mods.Loader.Env]),
		}
		for name := range game.Mods.Loader.Options {
			lc.Options = append(lc.Options, name)
		}
		config.Loader = lc
	}

	if config.Categories == nil {
		config.Categories = []games.ModCategoryDef{}
	}

	return config, nil
}

func (s *ModService) ListInstalled(ctx context.Context, gameserverID string) ([]model.InstalledMod, error) {
	return s.store.ListInstalledMods(gameserverID)
}

// Search searches all sources in a category, merges results.
func (s *ModService) Search(ctx context.Context, gameserverID, category, query string, offset, limit int) ([]ModResult, int, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, 0, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, 0, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, category)
	if cat == nil {
		return nil, 0, controller.ErrBadRequestf("category %q not available for this gameserver", category)
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)

	var allResults []ModResult
	totalHits := 0

	for _, src := range cat.Sources {
		catalog, ok := s.catalogs[src.Name]
		if !ok {
			continue
		}

		filters := s.buildFilters(src, gameVersion, loaderID)
		results, total, err := catalog.Search(ctx, query, filters, offset, limit)
		if err != nil {
			s.log.Warn("search failed for source", "source", src.Name, "error", err)
			continue
		}

		// Tag results with source name
		for i := range results {
			results[i].Source = src.Name
		}
		allResults = append(allResults, results...)
		totalHits += total
	}

	if allResults == nil {
		allResults = []ModResult{}
	}
	return allResults, totalHits, nil
}

// GetVersions returns versions for a specific mod from a specific source.
func (s *ModService) GetVersions(ctx context.Context, gameserverID, category, sourceName, sourceID string) ([]ModVersion, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, category)
	if cat == nil {
		return nil, controller.ErrBadRequestf("category %q not available", category)
	}

	src := findSource(cat, sourceName)
	if src == nil {
		return nil, controller.ErrBadRequestf("source %q not in category %q", sourceName, category)
	}

	catalog, ok := s.catalogs[sourceName]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", sourceName)
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	return catalog.GetVersions(ctx, sourceID, filters)
}

// --- Install ---

func (s *ModService) Install(ctx context.Context, gameserverID, category, sourceName, sourceID, versionID string) (*model.InstalledMod, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, category)
	if cat == nil {
		return nil, controller.ErrBadRequestf("category %q not available", category)
	}

	src := findSource(cat, sourceName)
	if src == nil {
		return nil, controller.ErrBadRequestf("source %q not in category %q", sourceName, category)
	}

	// Check duplicate
	existing, err := s.store.GetInstalledModBySource(gameserverID, sourceName, sourceID)
	if err != nil {
		return nil, fmt.Errorf("checking existing mod: %w", err)
	}
	if existing != nil {
		return nil, controller.ErrConflictf("mod %q is already installed", existing.Name)
	}

	catalog, ok := s.catalogs[sourceName]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", sourceName)
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	// Resolve version
	version, err := s.resolveVersion(ctx, catalog, sourceID, versionID, filters)
	if err != nil {
		return nil, err
	}

	// Pre-generate mod ID so dependencies can reference it
	modID := uuid.New().String()

	// Install dependencies (file delivery only)
	if src.Delivery == "file" {
		if err := s.installDependencies(ctx, gameserverID, modID, catalog, version, *src, category, filters, 0); err != nil {
			return nil, fmt.Errorf("installing dependencies: %w", err)
		}
	}

	// Deliver
	if err := s.deliver(ctx, gameserverID, *src, version); err != nil {
		return nil, err
	}

	// Record
	mod := s.newInstalledMod(gameserverID, sourceName, sourceID, category, version, src.Delivery, false, nil)
	mod.ID = modID
	if src.Delivery == "file" {
		mod.FilePath = fmt.Sprintf("%s/%s", src.InstallPath, sanitizeFileName(version.FileName))
		mod.FileName = sanitizeFileName(version.FileName)
	}
	if err := s.store.CreateInstalledMod(mod); err != nil {
		return nil, fmt.Errorf("saving installed mod: %w", err)
	}

	s.publishEvent(ctx, gameserverID, mod, controller.EventModInstalled)
	return mod, nil
}

// --- Uninstall ---

func (s *ModService) Uninstall(ctx context.Context, gameserverID, modID string) error {
	mod, err := s.store.GetInstalledMod(modID)
	if err != nil {
		return fmt.Errorf("getting installed mod: %w", err)
	}
	if mod == nil || mod.GameserverID != gameserverID {
		return controller.ErrNotFound("mod not found")
	}

	// If this mod is part of a pack, record an exclusion so pack updates don't re-add it
	if mod.PackID != nil && *mod.PackID != "" {
		s.store.CreatePackExclusion(&model.PackExclusion{
			PackModID:  *mod.PackID,
			SourceID:   mod.SourceID,
			ExcludedAt: time.Now(),
		})
	}

	// If this is a modpack itself, remove all mods linked to it
	if mod.Delivery == "pack" {
		if err := s.uninstallPackMods(ctx, gameserverID, modID); err != nil {
			return err
		}
	}

	// Deliver uninstall
	switch mod.Delivery {
	case "file":
		s.fileDel.Uninstall(ctx, gameserverID, mod.FilePath)
	case "manifest":
		// Rebuild manifest without this mod
		remaining := s.remainingManifestIDs(gameserverID, mod.Source, mod.SourceID)
		game := s.gameStore.GetGame(s.gameserverGameID(gameserverID))
		if manifestPath := s.findManifestPath(game, mod.Category, mod.Source); manifestPath != "" {
			s.manifestDel.Uninstall(ctx, gameserverID, manifestPath, remaining)
		}
	}

	if err := s.store.DeleteInstalledMod(modID); err != nil {
		return fmt.Errorf("deleting installed mod: %w", err)
	}

	// Clean up orphaned dependencies
	if mod.PackID == nil || *mod.PackID == "" {
		s.removeOrphanedDependencies(ctx, gameserverID, modID)
	}

	s.publishEvent(ctx, gameserverID, mod, controller.EventModUninstalled)
	return nil
}

// --- Update ---

func (s *ModService) CheckForUpdates(ctx context.Context, gameserverID string) ([]ModUpdate, error) {
	installed, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return nil, err
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, nil
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)

	var updates []ModUpdate
	for _, mod := range installed {
		if mod.Delivery == "manifest" || mod.AutoInstalled {
			continue // Workshop auto-updates, deps update with parent
		}

		catalog, ok := s.catalogs[mod.Source]
		if !ok {
			continue
		}

		cat := s.findCategory(game, gs.Env, mod.Category)
		if cat == nil {
			continue
		}
		src := findSource(cat, mod.Source)
		if src == nil {
			continue
		}

		filters := s.buildFilters(*src, gameVersion, loaderID)
		versions, err := catalog.GetVersions(ctx, mod.SourceID, filters)
		if err != nil || len(versions) == 0 {
			continue
		}

		latest := versions[0]
		if latest.VersionID != mod.VersionID {
			updates = append(updates, ModUpdate{
				ModID:          mod.ID,
				ModName:        mod.Name,
				CurrentVersion: mod.Version,
				LatestVersion:  latest,
			})
		}
	}
	return updates, nil
}

func (s *ModService) Update(ctx context.Context, gameserverID, modID string) (*model.InstalledMod, error) {
	mod, err := s.store.GetInstalledMod(modID)
	if err != nil || mod == nil {
		return nil, controller.ErrNotFound("mod not found")
	}
	if mod.GameserverID != gameserverID {
		return nil, controller.ErrNotFound("mod not found")
	}

	catalog, ok := s.catalogs[mod.Source]
	if !ok {
		return nil, controller.ErrBadRequestf("unknown source: %s", mod.Source)
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	cat := s.findCategory(game, gs.Env, mod.Category)
	if cat == nil {
		return nil, controller.ErrBadRequestf("category %q not available", mod.Category)
	}
	src := findSource(cat, mod.Source)
	if src == nil {
		return nil, controller.ErrBadRequestf("source %q not available", mod.Source)
	}

	loaderID := s.resolveLoaderID(game, gs.Env)
	gameVersion := s.resolveGameVersion(game, gs.Env)
	filters := s.buildFilters(*src, gameVersion, loaderID)

	versions, err := catalog.GetVersions(ctx, mod.SourceID, filters)
	if err != nil || len(versions) == 0 {
		return nil, fmt.Errorf("no versions available for %s", mod.Name)
	}

	latest := versions[0]
	if latest.VersionID == mod.VersionID {
		return mod, nil // already up to date
	}

	// Replace file
	if mod.Delivery == "file" && mod.FilePath != "" {
		s.fileDel.Uninstall(ctx, gameserverID, mod.FilePath)
		if err := s.fileDel.Install(ctx, gameserverID, src.InstallPath, latest.DownloadURL, latest.FileName); err != nil {
			return nil, fmt.Errorf("updating mod file: %w", err)
		}
		mod.FilePath = fmt.Sprintf("%s/%s", src.InstallPath, sanitizeFileName(latest.FileName))
		mod.FileName = sanitizeFileName(latest.FileName)
	}

	mod.Version = latest.Version
	mod.VersionID = latest.VersionID
	if err := s.store.UpdateModVersion(modID, latest.VersionID, latest.Version); err != nil {
		return nil, fmt.Errorf("updating mod record: %w", err)
	}

	return mod, nil
}

func (s *ModService) UpdateAll(ctx context.Context, gameserverID string) ([]ModUpdate, error) {
	updates, err := s.CheckForUpdates(ctx, gameserverID)
	if err != nil {
		return nil, err
	}

	var applied []ModUpdate
	for _, u := range updates {
		if _, err := s.Update(ctx, gameserverID, u.ModID); err != nil {
			s.log.Warn("failed to update mod", "mod_id", u.ModID, "error", err)
			continue
		}
		applied = append(applied, u)
	}
	return applied, nil
}

// --- Compatibility ---

func (s *ModService) CheckCompatibility(ctx context.Context, gameserverID string, newEnv model.Env) ([]ModIssue, error) {
	installed, err := s.store.ListInstalledMods(gameserverID)
	if err != nil {
		return nil, err
	}
	if len(installed) == 0 {
		return nil, nil
	}

	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, nil
	}

	// Check for loader change
	if game.Mods.Loader != nil {
		oldLoader := string(gs.Env[game.Mods.Loader.Env])
		newLoader := string(newEnv[game.Mods.Loader.Env])

		if oldLoader != newLoader {
			oldOpt := game.Mods.Loader.Options[oldLoader]
			newOpt := game.Mods.Loader.Options[newLoader]

			// Framework deactivation (e.g., Oxide off)
			if len(newOpt.ModSources) == 0 && len(oldOpt.ModSources) > 0 {
				var issues []ModIssue
				for _, mod := range installed {
					issues = append(issues, ModIssue{
						ModID:   mod.ID,
						ModName: mod.Name,
						Type:    "deactivated",
						Reason:  "Mod framework disabled. Mods will remain on disk but won't load.",
					})
				}
				return issues, nil
			}

			// Loader change (e.g., Fabric → Forge)
			if oldLoader != newLoader {
				var issues []ModIssue
				for _, mod := range installed {
					issues = append(issues, ModIssue{
						ModID:   mod.ID,
						ModName: mod.Name,
						Type:    "incompatible",
						Reason:  fmt.Sprintf("Changing loader from %s to %s invalidates all mods.", oldLoader, newLoader),
					})
				}
				return issues, nil
			}
		}
	}

	// Check for version change
	if game.Mods.VersionEnv != "" {
		oldVersion := string(gs.Env[game.Mods.VersionEnv])
		newVersion := string(newEnv[game.Mods.VersionEnv])

		if oldVersion != newVersion && newVersion != "" {
			// Check each installed mod against the new version
			var issues []ModIssue
			for _, mod := range installed {
				if mod.AutoInstalled || mod.Delivery == "manifest" {
					continue
				}
				catalog, ok := s.catalogs[mod.Source]
				if !ok {
					continue
				}

				cat := s.findCategory(game, newEnv, mod.Category)
				if cat == nil {
					issues = append(issues, ModIssue{
						ModID:   mod.ID,
						ModName: mod.Name,
						Type:    "incompatible",
						Reason:  fmt.Sprintf("Category %q not available with new config.", mod.Category),
					})
					continue
				}

				src := findSource(cat, mod.Source)
				if src == nil {
					continue
				}

				loaderID := s.resolveLoaderID(game, newEnv)
				filters := s.buildFilters(*src, newVersion, loaderID)
				versions, err := catalog.GetVersions(ctx, mod.SourceID, filters)
				if err != nil || len(versions) == 0 {
					issues = append(issues, ModIssue{
						ModID:   mod.ID,
						ModName: mod.Name,
						Type:    "incompatible",
						Reason:  fmt.Sprintf("No compatible version found for %s.", newVersion),
					})
				}
			}
			return issues, nil
		}
	}

	return nil, nil
}

// --- Internal helpers ---

func (s *ModService) getGameserver(gameserverID string) (*model.Gameserver, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver: %w", err)
	}
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return gs, nil
}

func (s *ModService) gameserverGameID(gameserverID string) string {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil || gs == nil {
		return ""
	}
	return gs.GameID
}

// availableCategories returns categories whose sources are available given current env.
func (s *ModService) availableCategories(game *games.Game, env model.Env) []games.ModCategoryDef {
	allowedSources := s.allowedSources(game, env)
	if allowedSources == nil {
		return game.Mods.Categories
	}

	var result []games.ModCategoryDef
	for _, cat := range game.Mods.Categories {
		if cat.AlwaysAvailable {
			result = append(result, cat)
			continue
		}
		var filtered []games.ModCategorySource
		for _, src := range cat.Sources {
			if allowedSources[src.Name] {
				filtered = append(filtered, src)
			}
		}
		if len(filtered) > 0 {
			result = append(result, games.ModCategoryDef{Name: cat.Name, AlwaysAvailable: cat.AlwaysAvailable, Sources: filtered})
		}
	}
	return result
}

// allowedSources returns which source names are allowed by the current loader config.
// Returns nil if no loader is configured (all sources allowed).
func (s *ModService) allowedSources(game *games.Game, env model.Env) map[string]bool {
	if game.Mods.Loader == nil {
		return nil // no loader = all sources available
	}

	loaderValue := string(env[game.Mods.Loader.Env])
	opt, ok := game.Mods.Loader.Options[loaderValue]
	if !ok {
		return map[string]bool{} // unknown loader value = no sources
	}

	allowed := make(map[string]bool, len(opt.ModSources))
	for _, name := range opt.ModSources {
		allowed[name] = true
	}
	return allowed
}

func (s *ModService) findCategory(game *games.Game, env model.Env, categoryName string) *games.ModCategoryDef {
	for _, cat := range s.availableCategories(game, env) {
		if cat.Name == categoryName {
			return &cat
		}
	}
	return nil
}

func findSource(cat *games.ModCategoryDef, sourceName string) *games.ModCategorySource {
	for i := range cat.Sources {
		if cat.Sources[i].Name == sourceName {
			return &cat.Sources[i]
		}
	}
	return nil
}

func (s *ModService) resolveLoaderID(game *games.Game, env model.Env) string {
	if game.Mods.Loader == nil {
		return ""
	}
	loaderValue := string(env[game.Mods.Loader.Env])
	if opt, ok := game.Mods.Loader.Options[loaderValue]; ok {
		return opt.LoaderID
	}
	return ""
}

func (s *ModService) resolveGameVersion(game *games.Game, env model.Env) string {
	if game.Mods.VersionEnv == "" {
		return ""
	}
	return string(env[game.Mods.VersionEnv])
}

func (s *ModService) buildFilters(src games.ModCategorySource, gameVersion, loaderID string) CatalogFilters {
	extra := make(map[string]string)
	for k, v := range src.Filters {
		extra[k] = v
	}
	for k, v := range src.Config {
		extra[k] = v
	}
	return CatalogFilters{
		GameVersion: gameVersion,
		Loader:      loaderID,
		Extra:       extra,
	}
}

func (s *ModService) resolveVersion(ctx context.Context, catalog ModCatalog, modID, versionID string, filters CatalogFilters) (*ModVersion, error) {
	versions, err := catalog.GetVersions(ctx, modID, filters)
	if err != nil {
		return nil, fmt.Errorf("getting versions: %w", err)
	}

	if versionID == "" {
		if len(versions) == 0 {
			return nil, controller.ErrBadRequest("no compatible versions found")
		}
		return &versions[0], nil
	}

	for i := range versions {
		if versions[i].VersionID == versionID {
			return &versions[i], nil
		}
	}
	return nil, controller.ErrBadRequestf("version %q not found", versionID)
}

func (s *ModService) deliver(ctx context.Context, gameserverID string, src games.ModCategorySource, version *ModVersion) error {
	switch src.Delivery {
	case "file":
		return s.fileDel.Install(ctx, gameserverID, src.InstallPath, version.DownloadURL, sanitizeFileName(version.FileName))
	case "manifest":
		manifestPath := src.Config["manifest_path"]
		if manifestPath == "" {
			return fmt.Errorf("manifest delivery requires manifest_path in source config")
		}
		allIDs := s.allManifestIDs(gameserverID, src.Name, version.VersionID)
		return s.manifestDel.Install(ctx, gameserverID, manifestPath, allIDs)
	default:
		return fmt.Errorf("unsupported delivery type: %s", src.Delivery)
	}
}

func (s *ModService) allManifestIDs(gameserverID, sourceName, newID string) []string {
	installed, _ := s.store.ListInstalledMods(gameserverID)
	var ids []string
	for _, m := range installed {
		if m.Source == sourceName && m.Delivery == "manifest" {
			ids = append(ids, m.SourceID)
		}
	}
	ids = append(ids, newID)
	return ids
}

func (s *ModService) remainingManifestIDs(gameserverID, sourceName, excludeSourceID string) []string {
	installed, _ := s.store.ListInstalledMods(gameserverID)
	var ids []string
	for _, m := range installed {
		if m.Source == sourceName && m.Delivery == "manifest" && m.SourceID != excludeSourceID {
			ids = append(ids, m.SourceID)
		}
	}
	return ids
}

func (s *ModService) findManifestPath(game *games.Game, category, sourceName string) string {
	if game == nil {
		return ""
	}
	for _, cat := range game.Mods.Categories {
		if cat.Name == category {
			for _, src := range cat.Sources {
				if src.Name == sourceName {
					return src.Config["manifest_path"]
				}
			}
		}
	}
	return ""
}

func (s *ModService) newInstalledMod(gameserverID, source, sourceID, category string, version *ModVersion, delivery string, autoInstalled bool, packID *string) *model.InstalledMod {
	return &model.InstalledMod{
		ID:            uuid.New().String(),
		GameserverID:  gameserverID,
		Source:        source,
		SourceID:      sourceID,
		Category:      category,
		Name:          version.FileName,
		Version:       version.Version,
		VersionID:     version.VersionID,
		Delivery:      delivery,
		AutoInstalled: autoInstalled,
		PackID:        packID,
		Metadata:      json.RawMessage(`{}`),
		InstalledAt:   time.Now(),
	}
}

func (s *ModService) publishEvent(ctx context.Context, gameserverID string, mod *model.InstalledMod, eventType string) {
	s.broadcaster.Publish(controller.ModActionEvent{
		Type:         eventType,
		Timestamp:    time.Now(),
		Actor:        controller.ActorFromContext(ctx),
		GameserverID: gameserverID,
		Mod:          mod,
	})
}

func (s *ModService) uninstallPackMods(ctx context.Context, gameserverID, packModID string) error {
	mods, err := s.store.ListModsByPackID(gameserverID, packModID)
	if err != nil {
		return fmt.Errorf("listing pack mods: %w", err)
	}
	for _, mod := range mods {
		if mod.Delivery == "file" {
			s.fileDel.Uninstall(ctx, gameserverID, mod.FilePath)
		}
		s.store.DeleteInstalledMod(mod.ID)
	}
	return nil
}

// sanitizeFileName strips path components and prevents directory traversal.
func sanitizeFileName(name string) string {
	name = pathBase(name)
	name = strings.ReplaceAll(name, "..", "")
	name = strings.ReplaceAll(name, "/", "")
	name = strings.ReplaceAll(name, "\\", "")
	if name == "." || name == "" {
		return ""
	}
	return name
}

func pathBase(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	return p
}
