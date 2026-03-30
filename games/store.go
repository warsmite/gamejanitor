package games

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ── Shared field types — used in both GameDef (registry) and Game (store) ──

type Port struct {
	Name     string `yaml:"name" json:"name"`
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

type EnvVar struct {
	Key             string          `yaml:"key" json:"key"`
	Default         string          `yaml:"default" json:"default"`
	Label           string          `yaml:"label,omitempty" json:"label,omitempty"`
	Type            string          `yaml:"type,omitempty" json:"type,omitempty"`
	Group           string          `yaml:"group,omitempty" json:"group,omitempty"`
	Options         []string        `yaml:"options,omitempty" json:"options,omitempty"`
	DynamicOptions  *DynamicOptions `yaml:"dynamic_options,omitempty" json:"dynamic_options,omitempty"`
	Required        bool            `yaml:"required,omitempty" json:"required,omitempty"` // unused by built-in games — all have defaults. For custom games needing user-provided values (e.g. FiveM license key).
	ConsentRequired bool            `yaml:"consent_required,omitempty" json:"consent_required,omitempty"`
	Notice          string          `yaml:"notice,omitempty" json:"notice,omitempty"`
	Autogenerate    string          `yaml:"autogenerate,omitempty" json:"autogenerate,omitempty"`
	System          bool            `yaml:"system,omitempty" json:"system,omitempty"`
	Hidden          bool            `yaml:"hidden,omitempty" json:"hidden,omitempty"`
	TriggersInstall bool            `yaml:"triggers_install,omitempty" json:"triggers_install,omitempty"`
}

// DynamicOptions configures a backend provider that supplies options at runtime.
// Used for things like Minecraft version lists that change over time.
type DynamicOptions struct {
	Source string         `yaml:"source" json:"source"`
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

// RuntimeConfig controls image variant resolution.
// Static images set Image directly. Dynamic resolution (e.g., Minecraft → Java version)
// uses a named Resolver that maps env values to image tags.
type RuntimeConfig struct {
	Image        string            `yaml:"image,omitempty" json:"image,omitempty"`
	Resolver     string            `yaml:"resolver,omitempty" json:"resolver,omitempty"`
	DefaultImage string            `yaml:"default_image,omitempty" json:"default_image,omitempty"`
	Images       map[string]string `yaml:"images,omitempty" json:"images,omitempty"`
}

// ModsConfig defines the full mod system configuration for a game.
// Version/loader pickers and mod categories are all declared here.
type ModsConfig struct {
	VersionEnv string           `yaml:"version_env,omitempty" json:"version_env,omitempty"`
	Loader     *ModLoaderDef    `yaml:"loader,omitempty" json:"loader,omitempty"`
	Categories []ModCategoryDef `yaml:"categories,omitempty" json:"categories,omitempty"`
}

// ModLoaderDef defines the loader/framework selector (e.g., Fabric/Forge/Paper for MC,
// Oxide on/off for Rust). The env var controls the loader, and each option specifies
// which mod sources are available when that loader is active.
type ModLoaderDef struct {
	Env     string                    `yaml:"env" json:"env"`
	Options map[string]ModLoaderOption `yaml:"options" json:"options"`
}

// ModLoaderOption defines which mod sources are available for a given loader value.
type ModLoaderOption struct {
	ModSources []string `yaml:"mod_sources" json:"mod_sources"`
	LoaderID   string   `yaml:"loader_id,omitempty" json:"loader_id,omitempty"`
}

// ModCategoryDef defines a tab in the mod UI (e.g., "Mods", "Resource Packs", "Modpacks").
// ModCategoryDef defines a tab in the mod UI (e.g., "Mods", "Resource Packs", "Modpacks").
// AlwaysAvailable categories are shown regardless of the current loader (e.g., resource packs
// work on vanilla Minecraft). Loader-gated categories (default) are only shown when the
// loader allows their sources.
type ModCategoryDef struct {
	Name            string             `yaml:"name" json:"name"`
	AlwaysAvailable bool               `yaml:"always_available,omitempty" json:"always_available,omitempty"`
	InstallPath     string             `yaml:"install_path,omitempty" json:"install_path,omitempty"`
	Sources         []ModCategorySource `yaml:"sources" json:"sources"`
}

// ResolveInstallPath returns the install path for a source, falling back to the category-level path.
// This allows categories with no catalog sources to still define where mods go (scan, upload, URL install).
func (c ModCategoryDef) ResolveInstallPath(src *ModCategorySource) string {
	if src != nil && src.InstallPath != "" {
		return src.InstallPath
	}
	return c.InstallPath
}

// ModCategorySource configures one mod source within a category.
type ModCategorySource struct {
	Name          string            `yaml:"name" json:"name"`
	Delivery      string            `yaml:"delivery" json:"delivery"` // "file", "manifest", "pack"
	InstallPath   string            `yaml:"install_path,omitempty" json:"install_path,omitempty"`
	OverridesPath string            `yaml:"overrides_path,omitempty" json:"overrides_path,omitempty"`
	Filters       map[string]string `yaml:"filters,omitempty" json:"filters,omitempty"`
	Config        map[string]string `yaml:"config,omitempty" json:"config,omitempty"`
}

type Assets struct {
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`
}

// ── Game — flattened runtime type used by gamejanitor services ──

// Game is the runtime representation of a game with container support.
// Gamejanitor services use this type — fields are flattened from GameDef.Container
// for ergonomic access (game.BaseImage instead of game.Container.Image).
type Game struct {
	ID                   string        `json:"id"`
	Name                 string        `json:"name"`
	Aliases              []string      `json:"aliases,omitempty"`
	Description          string        `json:"description,omitempty"`
	AppID                uint32         `json:"app_id,omitempty"`
	ServerAppID          uint32         `json:"server_app_id,omitempty"`
	SteamLogin           SteamLoginType `json:"steam_login,omitempty"`
	BaseImage            string         `json:"base_image"`
	Runtime              RuntimeConfig `json:"runtime,omitempty"`
	IconPath             string        `json:"icon_path"`
	DefaultPorts         []Port        `json:"default_ports"`
	DefaultEnv           []EnvVar      `json:"default_env"`
	RecommendedMemoryMB  int           `json:"recommended_memory_mb"`
	ReadyPattern         string        `json:"ready_pattern,omitempty"`
	DisabledCapabilities []string      `json:"disabled_capabilities"`
	Mods                 ModsConfig    `json:"mods,omitempty"`
	Query                *QueryConfig  `json:"query,omitempty"`
}

// DepotAppID returns the app ID to use for depot downloads.
// Uses ServerAppID if set, otherwise falls back to AppID.
func (g *Game) DepotAppID() uint32 {
	if g.ServerAppID != 0 {
		return g.ServerAppID
	}
	return g.AppID
}

// HasCapability returns true if the capability is NOT in the game's DisabledCapabilities list.
func (g *Game) HasCapability(capability string) bool {
	for _, cap := range g.DisabledCapabilities {
		if cap == capability {
			return false
		}
	}
	return true
}

// ── GameStore — gamejanitor-specific, wraps Registry ──

// GameStore provides game data for gamejanitor services. It only exposes games
// with container support (image, env, etc.) and adds local override loading,
// script extraction, and asset serving on top of the shared Registry.
type GameStore struct {
	registry *Registry
	games    map[string]*Game
	aliases  map[string]string // alias → game ID
	gameFS   map[string]fs.FS
	sorted   []Game
	log      *slog.Logger
	localDir string
}

// Registry returns the underlying shared registry for query-config lookups.
// gjq and other consumers use this for protocol/port data on all games,
// including those without container support.
func (s *GameStore) Registry() *Registry {
	return s.registry
}

func NewGameStore(localGamesDir string, log *slog.Logger) (*GameStore, error) {
	registry, err := NewRegistry()
	if err != nil {
		return nil, fmt.Errorf("loading game registry: %w", err)
	}

	s := &GameStore{
		registry: registry,
		games:    make(map[string]*Game),
		aliases:  make(map[string]string),
		gameFS:   make(map[string]fs.FS),
		log:      log,
		localDir: localGamesDir,
	}

	// Load container games from embedded data
	embeddedRoot, err := fs.Sub(embeddedGames, "data")
	if err != nil {
		return nil, fmt.Errorf("accessing embedded game data: %w", err)
	}

	if err := s.loadGamesFromFS(embeddedRoot, "embedded"); err != nil {
		return nil, fmt.Errorf("loading embedded games: %w", err)
	}

	// Load local overrides (higher priority, overwrites embedded)
	if localGamesDir != "" {
		if info, err := os.Stat(localGamesDir); err == nil && info.IsDir() {
			localFS := os.DirFS(localGamesDir)
			if err := s.loadGamesFromFS(localFS, "local"); err != nil {
				return nil, fmt.Errorf("loading local games from %s: %w", localGamesDir, err)
			}
		}
	}

	// Build alias map and sorted list
	s.sorted = make([]Game, 0, len(s.games))
	for _, g := range s.games {
		s.sorted = append(s.sorted, *g)
		for _, alias := range g.Aliases {
			if existing, ok := s.aliases[alias]; ok {
				log.Warn("duplicate game alias, ignoring", "alias", alias, "game", g.ID, "existing_game", existing)
				continue
			}
			s.aliases[alias] = g.ID
		}
	}
	sort.Slice(s.sorted, func(i, j int) bool {
		return s.sorted[i].Name < s.sorted[j].Name
	})

	log.Info("game store loaded",
		"container_games", len(s.games),
		"total_registry", registry.Count(),
		"alias_count", len(s.aliases),
	)
	return s, nil
}

func (s *GameStore) loadGamesFromFS(root fs.FS, source string) error {
	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return fmt.Errorf("reading game directories: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "_default" {
			continue
		}

		gameDir := entry.Name()
		yamlData, err := fs.ReadFile(root, filepath.Join(gameDir, "game.yaml"))
		if err != nil {
			continue // skip directories without game.yaml
		}

		var def GameDef
		if err := yaml.Unmarshal(yamlData, &def); err != nil {
			return fmt.Errorf("parsing game.yaml for %s: %w", gameDir, err)
		}

		if def.ID == "" {
			def.ID = gameDir
		}

		// Only load games with container support into the GameStore
		if !def.HasContainer() {
			continue
		}

		game := defToGame(&def)

		if def.Assets.Icon != "" {
			game.IconPath = "/assets/games/" + def.ID + "/" + def.Assets.Icon
		}

		subFS, err := fs.Sub(root, gameDir)
		if err != nil {
			return fmt.Errorf("creating sub-fs for game %s: %w", def.ID, err)
		}

		s.games[def.ID] = game
		s.gameFS[def.ID] = subFS
		s.log.Debug("loaded game", "id", def.ID, "name", def.Name, "source", source)
	}

	return nil
}

// defToGame flattens a GameDef into the runtime Game type.
func defToGame(def *GameDef) *Game {
	c := def.Container

	caps := c.DisabledCapabilities
	if caps == nil {
		caps = []string{}
	}

	ports := def.Ports
	if ports == nil {
		ports = []Port{}
	}

	env := c.Env
	if env == nil {
		env = []EnvVar{}
	}

	mods := c.Mods
	if mods.Categories == nil {
		mods.Categories = []ModCategoryDef{}
	}

	runtime := RuntimeConfig{}
	if c.Runtime != nil {
		runtime = *c.Runtime
	}

	aliases := def.Aliases
	if aliases == nil {
		aliases = []string{}
	}

	return &Game{
		ID:                   def.ID,
		Name:                 def.Name,
		Aliases:              aliases,
		Description:          def.Description,
		AppID:                def.AppID,
		ServerAppID:          def.ServerAppID,
		SteamLogin:           def.SteamLogin,
		BaseImage:            c.Image,
		Runtime:              runtime,
		RecommendedMemoryMB:  c.RecommendedMemoryMB,
		ReadyPattern:         c.ReadyPattern,
		DefaultPorts:         ports,
		DefaultEnv:           env,
		DisabledCapabilities: caps,
		Mods:                 mods,
		Query:                def.Query,
	}
}

func (s *GameStore) ListGames() []Game {
	return s.sorted
}

// ResolveGameID resolves an alias to the canonical game ID.
// Returns the input unchanged if it's already a canonical ID or not found.
func (s *GameStore) ResolveGameID(id string) string {
	if _, ok := s.games[id]; ok {
		return id
	}
	if realID, ok := s.aliases[id]; ok {
		return realID
	}
	return id
}

func (s *GameStore) GetGame(id string) *Game {
	if g, ok := s.games[id]; ok {
		return g
	}
	if realID, ok := s.aliases[id]; ok {
		return s.games[realID]
	}
	return nil
}

// GetGameFS returns the filesystem for a game's directory (scripts/, assets/, defaults/).
func (s *GameStore) GetGameFS(id string) fs.FS {
	if f, ok := s.gameFS[id]; ok {
		return f
	}
	if realID, ok := s.aliases[id]; ok {
		return s.gameFS[realID]
	}
	return nil
}

// AssetsFS returns an fs.FS that serves game assets at {gameID}/{filename}
// for use with the /assets/games/ route. Includes the _default fallback.
func (s *GameStore) AssetsFS() fs.FS {
	return &gameAssetsFS{store: s}
}

type gameAssetsFS struct {
	store *GameStore
}

func (f *gameAssetsFS) Open(name string) (fs.File, error) {
	dir, file := filepath.Split(name)
	dir = filepath.Clean(dir)

	if dir == "default" || dir == "_default" {
		embeddedRoot, err := fs.Sub(embeddedGames, "data/_default/assets")
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		return embeddedRoot.Open(file)
	}

	gameFS := f.store.GetGameFS(dir)
	if gameFS == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	return gameFS.Open(filepath.Join("assets", file))
}

// ExtractScripts writes a game's scripts and defaults to a local directory.
// Called before starting a container so scripts can be bind-mounted.
func (s *GameStore) ExtractScripts(gameID, targetDir string) error {
	gameFS := s.GetGameFS(gameID)
	if gameFS == nil {
		return fmt.Errorf("game %s not found", gameID)
	}

	scriptsDir := filepath.Join(targetDir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0755); err != nil {
		return fmt.Errorf("creating scripts directory: %w", err)
	}

	if err := extractDir(gameFS, "scripts", scriptsDir, 0755); err != nil {
		return fmt.Errorf("extracting scripts: %w", err)
	}

	if _, err := fs.Stat(gameFS, "defaults"); err == nil {
		defaultsDir := filepath.Join(targetDir, "defaults")
		if err := os.MkdirAll(defaultsDir, 0755); err != nil {
			return fmt.Errorf("creating defaults directory: %w", err)
		}
		if err := extractDir(gameFS, "defaults", defaultsDir, 0644); err != nil {
			return fmt.Errorf("extracting defaults: %w", err)
		}
	}

	return nil
}

func extractDir(srcFS fs.FS, srcDir, dstDir string, perm os.FileMode) error {
	entries, err := fs.ReadDir(srcFS, srcDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := fs.ReadFile(srcFS, filepath.Join(srcDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		dstPath := filepath.Join(dstDir, entry.Name())
		if err := os.WriteFile(dstPath, data, perm); err != nil {
			return fmt.Errorf("writing %s: %w", dstPath, err)
		}
	}

	return nil
}
