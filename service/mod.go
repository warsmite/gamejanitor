package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/models"
)

type ModService struct {
	db              *sql.DB
	fileSvc         *FileService
	gameStore       *games.GameStore
	settingsSvc     *SettingsService
	optionsRegistry *games.OptionsRegistry
	sources         map[string]ModSource
	httpClient      *http.Client
	log             *slog.Logger
	broadcaster     *EventBus
}

func NewModService(db *sql.DB, fileSvc *FileService, gameStore *games.GameStore, settingsSvc *SettingsService, optionsRegistry *games.OptionsRegistry, broadcaster *EventBus, log *slog.Logger) *ModService {
	sources := map[string]ModSource{
		"umod":     NewUmodSource(log.With("source", "umod")),
		"modrinth": NewModrinthSource(log.With("source", "modrinth")),
		"workshop": NewWorkshopSource(settingsSvc, log.With("source", "workshop")),
	}

	return &ModService{
		db:              db,
		fileSvc:         fileSvc,
		gameStore:       gameStore,
		settingsSvc:     settingsSvc,
		optionsRegistry: optionsRegistry,
		sources:         sources,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		log:             log,
		broadcaster: broadcaster,
	}
}

// RegisterSource adds a mod source (used to add modrinth, workshop after construction).
func (s *ModService) RegisterSource(sourceType string, source ModSource) {
	s.sources[sourceType] = source
}

// GetSources returns the available mod sources for a gameserver, including search mode info.
func (s *ModService) GetSources(ctx context.Context, gameserverID string) ([]ModSourceInfo, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return nil, ErrNotFoundf("game %s not found", gs.GameID)
	}

	var infos []ModSourceInfo
	for _, src := range game.Mods.Sources {
		info := ModSourceInfo{
			Type:       src.Type,
			SearchMode: "search",
		}
		if src.Type == "workshop" {
			key := s.settingsSvc.GetString("steam_api_key")
			if key == "" {
				info.SearchMode = "paste_id"
			}
		}
		// Resolve the current game version for this source
		gameVersion, _ := s.resolveFilters(gs, &src)
		info.GameVersion = gameVersion
		infos = append(infos, info)
	}
	if infos == nil {
		infos = []ModSourceInfo{}
	}
	return infos, nil
}

func (s *ModService) Search(ctx context.Context, gameserverID string, sourceType string, query string, offset int, limit int) ([]ModSearchResult, int, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, 0, err
	}

	srcConfig, err := s.getSourceConfig(gs.GameID, sourceType)
	if err != nil {
		return nil, 0, err
	}

	source, ok := s.sources[sourceType]
	if !ok {
		return nil, 0, ErrBadRequestf("unsupported mod source: %s", sourceType)
	}

	gameVersion, loader := s.resolveFilters(gs, srcConfig)

	// Workshop uses loader field to pass appID for search filtering
	if sourceType == "workshop" && srcConfig.AppID > 0 {
		loader = strconv.Itoa(srcConfig.AppID)
	}

	results, total, err := source.Search(ctx, query, gameVersion, loader, offset, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("searching %s: %w", sourceType, err)
	}
	return results, total, nil
}

func (s *ModService) GetVersions(ctx context.Context, gameserverID string, sourceType string, sourceID string) ([]ModVersion, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}

	srcConfig, err := s.getSourceConfig(gs.GameID, sourceType)
	if err != nil {
		return nil, err
	}

	source, ok := s.sources[sourceType]
	if !ok {
		return nil, ErrBadRequestf("unsupported mod source: %s", sourceType)
	}

	gameVersion, loader := s.resolveFilters(gs, srcConfig)

	versions, err := source.GetVersions(ctx, sourceID, gameVersion, loader)
	if err != nil {
		return nil, fmt.Errorf("getting versions from %s: %w", sourceType, err)
	}
	return versions, nil
}

func (s *ModService) Install(ctx context.Context, gameserverID string, sourceType string, sourceID string, versionID string, displayName string) (*models.InstalledMod, error) {
	gs, err := s.getGameserver(gameserverID)
	if err != nil {
		return nil, err
	}

	srcConfig, err := s.getSourceConfig(gs.GameID, sourceType)
	if err != nil {
		return nil, err
	}

	if err := s.checkPreconditions(gs, srcConfig); err != nil {
		return nil, err
	}

	// Check if already installed
	existing, err := models.GetInstalledModBySource(s.db, gameserverID, sourceType, sourceID)
	if err != nil {
		return nil, fmt.Errorf("checking existing mod: %w", err)
	}
	if existing != nil {
		return nil, ErrConflictf("mod %q is already installed", existing.Name)
	}

	source, ok := s.sources[sourceType]
	if !ok {
		return nil, ErrBadRequestf("unsupported mod source: %s", sourceType)
	}

	gameVersion, loader := s.resolveFilters(gs, srcConfig)

	if sourceType == "workshop" {
		return s.installWorkshop(ctx, gs, srcConfig, source, sourceID, displayName)
	}

	// For direct-download sources (modrinth, umod): get version info, download, write file
	versions, err := source.GetVersions(ctx, sourceID, gameVersion, loader)
	if err != nil {
		return nil, fmt.Errorf("getting versions: %w", err)
	}

	var targetVersion *ModVersion
	if versionID == "" {
		// Install latest
		if len(versions) == 0 {
			return nil, ErrBadRequest("no compatible versions found")
		}
		targetVersion = &versions[0]
	} else {
		for i := range versions {
			if versions[i].VersionID == versionID {
				targetVersion = &versions[i]
				break
			}
		}
		if targetVersion == nil {
			return nil, ErrBadRequestf("version %q not found", versionID)
		}
	}

	// Download the file
	content, err := s.downloadFile(ctx, targetVersion.DownloadURL)
	if err != nil {
		return nil, fmt.Errorf("downloading mod: %w", err)
	}

	// Resolve install path and filename
	installPath := s.resolveInstallPath(srcConfig, loader)
	fileName := sanitizeFileName(targetVersion.FileName)
	if fileName == "" {
		return nil, ErrBadRequest("mod has no valid filename")
	}
	fullPath := path.Join(installPath, fileName)

	// Ensure the install directory exists
	_ = s.fileSvc.CreateDirectory(ctx, gameserverID, installPath)

	// Write file to gameserver volume
	if err := s.fileSvc.WriteFile(ctx, gameserverID, fullPath, content); err != nil {
		return nil, fmt.Errorf("writing mod file: %w", err)
	}

	s.log.Info("mod installed", "gameserver_id", gameserverID, "source", sourceType, "source_id", sourceID, "file", fullPath)

	// Build metadata from the search result / version info
	metadata := map[string]string{
		"slug": sourceID,
	}
	metadataJSON, _ := json.Marshal(metadata)

	modName := displayName
	if modName == "" {
		modName = targetVersion.FileName
	}

	mod := &models.InstalledMod{
		ID:           uuid.New().String(),
		GameserverID: gameserverID,
		Source:       sourceType,
		SourceID:     sourceID,
		Name:         modName,
		Version:      targetVersion.Version,
		VersionID:    targetVersion.VersionID,
		FilePath:     fullPath,
		FileName:     fileName,
		Metadata:     metadataJSON,
		InstalledAt:  time.Now(),
	}

	if err := models.CreateInstalledMod(s.db, mod); err != nil {
		return nil, fmt.Errorf("saving installed mod: %w", err)
	}

	s.broadcaster.Publish(ModActionEvent{
		Type:         EventModInstalled,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gameserverID,
		Mod:          mod,
	})

	return mod, nil
}

func (s *ModService) Uninstall(ctx context.Context, gameserverID string, modID string) error {
	mod, err := models.GetInstalledMod(s.db, modID)
	if err != nil {
		return fmt.Errorf("getting installed mod: %w", err)
	}
	if mod == nil {
		return ErrNotFound("mod not found")
	}
	if mod.GameserverID != gameserverID {
		return ErrNotFound("mod not found")
	}

	if mod.Source == "workshop" {
		if err := s.uninstallWorkshop(ctx, gameserverID, mod); err != nil {
			return err
		}
	} else if mod.FilePath != "" {
		// Delete the mod file from the volume
		if err := s.fileSvc.DeletePath(ctx, gameserverID, mod.FilePath); err != nil {
			s.log.Warn("failed to delete mod file, removing DB record anyway", "file", mod.FilePath, "error", err)
		}
	}

	if err := models.DeleteInstalledMod(s.db, modID); err != nil {
		return fmt.Errorf("deleting installed mod: %w", err)
	}

	s.log.Info("mod uninstalled", "gameserver_id", gameserverID, "mod_id", modID, "source", mod.Source, "name", mod.Name)

	s.broadcaster.Publish(ModActionEvent{
		Type:         EventModUninstalled,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gameserverID,
		Mod:          mod,
	})

	return nil
}

func (s *ModService) ListInstalled(ctx context.Context, gameserverID string) ([]models.InstalledMod, error) {
	return models.ListInstalledMods(s.db, gameserverID)
}

// --- Workshop helpers ---

func (s *ModService) installWorkshop(ctx context.Context, gs *models.Gameserver, srcConfig *games.ModSourceConfig, source ModSource, sourceID string, displayName string) (*models.InstalledMod, error) {
	// Fetch metadata about the workshop item
	versions, err := source.GetVersions(ctx, sourceID, "", "")
	if err != nil {
		return nil, fmt.Errorf("fetching workshop item details: %w", err)
	}

	name := displayName
	if name == "" {
		name = sourceID
	}
	if len(versions) > 0 && versions[0].FileName != "" {
		name = versions[0].FileName
	}

	metadata := map[string]string{
		"workshop_id": sourceID,
	}
	metadataJSON, _ := json.Marshal(metadata)

	mod := &models.InstalledMod{
		ID:           uuid.New().String(),
		GameserverID: gs.ID,
		Source:       "workshop",
		SourceID:     sourceID,
		Name:         name,
		Version:      "",
		VersionID:    "",
		FilePath:     "",
		FileName:     "",
		Metadata:     metadataJSON,
		InstalledAt:  time.Now(),
	}

	if err := models.CreateInstalledMod(s.db, mod); err != nil {
		return nil, fmt.Errorf("saving workshop mod: %w", err)
	}

	// Update the workshop manifest file on the volume
	if err := s.writeWorkshopManifest(ctx, gs.ID); err != nil {
		// Non-fatal: DB record exists, manifest will be written on next attempt
		s.log.Warn("failed to write workshop manifest", "gameserver_id", gs.ID, "error", err)
	}

	s.broadcaster.Publish(ModActionEvent{
		Type:         EventModInstalled,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gs.ID,
		Mod:          mod,
	})

	return mod, nil
}

func (s *ModService) uninstallWorkshop(ctx context.Context, gameserverID string, mod *models.InstalledMod) error {
	// Rewrite the manifest after removing this mod (DB row deleted by caller after this returns)
	// We need to read current mods, exclude this one, and rewrite
	mods, err := models.ListInstalledMods(s.db, gameserverID)
	if err != nil {
		return fmt.Errorf("listing mods for manifest update: %w", err)
	}

	var workshopIDs []string
	for _, m := range mods {
		if m.Source == "workshop" && m.ID != mod.ID {
			workshopIDs = append(workshopIDs, m.SourceID)
		}
	}

	manifestJSON, _ := json.Marshal(workshopIDs)
	manifestPath := "/data/.gamejanitor/workshop_items.json"

	if len(workshopIDs) == 0 {
		// Delete the manifest if no workshop mods remain
		_ = s.fileSvc.DeletePath(ctx, gameserverID, manifestPath)
	} else {
		if err := s.fileSvc.WriteFile(ctx, gameserverID, manifestPath, manifestJSON); err != nil {
			s.log.Warn("failed to update workshop manifest", "gameserver_id", gameserverID, "error", err)
		}
	}

	return nil
}

func (s *ModService) writeWorkshopManifest(ctx context.Context, gameserverID string) error {
	mods, err := models.ListInstalledMods(s.db, gameserverID)
	if err != nil {
		return err
	}

	var workshopIDs []string
	for _, m := range mods {
		if m.Source == "workshop" {
			workshopIDs = append(workshopIDs, m.SourceID)
		}
	}

	manifestJSON, _ := json.Marshal(workshopIDs)

	// Ensure the .gamejanitor directory exists
	_ = s.fileSvc.CreateDirectory(ctx, gameserverID, "/data/.gamejanitor")

	return s.fileSvc.WriteFile(ctx, gameserverID, "/data/.gamejanitor/workshop_items.json", manifestJSON)
}

// --- Internal helpers ---

func (s *ModService) getGameserver(gameserverID string) (*models.Gameserver, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, fmt.Errorf("getting gameserver: %w", err)
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return gs, nil
}

func (s *ModService) getSourceConfig(gameID string, sourceType string) (*games.ModSourceConfig, error) {
	game := s.gameStore.GetGame(gameID)
	if game == nil {
		return nil, ErrNotFoundf("game %s not found", gameID)
	}

	for i := range game.Mods.Sources {
		if game.Mods.Sources[i].Type == sourceType {
			return &game.Mods.Sources[i], nil
		}
	}
	return nil, ErrBadRequestf("game %s does not support mod source %q", game.Name, sourceType)
}

func (s *ModService) checkPreconditions(gs *models.Gameserver, srcConfig *games.ModSourceConfig) error {
	if len(srcConfig.RequiresEnv) == 0 && len(srcConfig.Loaders) == 0 {
		return nil
	}

	env := s.parseEnv(gs)

	// Check requires_env (e.g., OXIDE_ENABLED=true)
	for key, required := range srcConfig.RequiresEnv {
		actual := env[key]
		if actual != required {
			return ErrBadRequestf("requires %s to be %q (currently %q)", key, required, actual)
		}
	}

	// Check loader is valid (e.g., MODLOADER must be fabric/forge/paper, not vanilla)
	if srcConfig.LoaderEnv != "" && len(srcConfig.Loaders) > 0 {
		loaderValue := env[srcConfig.LoaderEnv]
		if _, ok := srcConfig.Loaders[loaderValue]; !ok {
			validLoaders := make([]string, 0, len(srcConfig.Loaders))
			for k := range srcConfig.Loaders {
				validLoaders = append(validLoaders, k)
			}
			return ErrBadRequestf("mod loader %q does not support mods (use %s)", loaderValue, strings.Join(validLoaders, ", "))
		}
	}

	return nil
}

func (s *ModService) resolveFilters(gs *models.Gameserver, srcConfig *games.ModSourceConfig) (string, string) {
	env := s.parseEnv(gs)

	var gameVersion, loader string
	if srcConfig.VersionEnv != "" {
		gameVersion = env[srcConfig.VersionEnv]

		// Resolve alias values like "latest" → "1.21.11" via the options registry.
		// The version env var's dynamic_options source (e.g., "mojang-versions") knows how to resolve these.
		if s.optionsRegistry != nil {
			game := s.gameStore.GetGame(gs.GameID)
			if game != nil {
				for _, e := range game.DefaultEnv {
					if e.Key == srcConfig.VersionEnv && e.DynamicOptions != nil {
						gameVersion = s.optionsRegistry.ResolveValue(e.DynamicOptions.Source, gameVersion)
						break
					}
				}
			}
		}
	}
	if srcConfig.LoaderEnv != "" {
		loaderValue := env[srcConfig.LoaderEnv]
		if mapped, ok := srcConfig.Loaders[loaderValue]; ok {
			loader = mapped
		}
	}
	return gameVersion, loader
}

func (s *ModService) resolveInstallPath(srcConfig *games.ModSourceConfig, loader string) string {
	if loader != "" && len(srcConfig.InstallPaths) > 0 {
		if p, ok := srcConfig.InstallPaths[loader]; ok {
			return p
		}
	}
	return srcConfig.InstallPath
}

func (s *ModService) parseEnv(gs *models.Gameserver) map[string]string {
	env := make(map[string]string)
	if err := json.Unmarshal(gs.Env, &env); err != nil {
		s.log.Warn("failed to parse gameserver env", "gameserver_id", gs.ID, "error", err)
	}
	return env
}

func (s *ModService) downloadFile(ctx context.Context, downloadURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating download request: %w", err)
	}
	req.Header.Set("User-Agent", "gamejanitor")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, constants.MaxModDownloadBytes))
	if err != nil {
		return nil, fmt.Errorf("reading download: %w", err)
	}
	return data, nil
}

// sanitizeFileName strips path components and prevents directory traversal.
func sanitizeFileName(name string) string {
	// Take only the base name
	name = path.Base(name)
	// Remove any remaining traversal
	name = strings.ReplaceAll(name, "..", "")
	name = strings.ReplaceAll(name, "/", "")
	name = strings.ReplaceAll(name, "\\", "")
	if name == "." || name == "" {
		return ""
	}
	return name
}
