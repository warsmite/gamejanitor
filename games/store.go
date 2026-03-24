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
	Required        bool            `yaml:"required,omitempty" json:"required,omitempty"`
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

type ModSourceConfig struct {
	Type          string            `yaml:"type" json:"type"`
	InstallPath   string            `yaml:"install_path,omitempty" json:"install_path,omitempty"`
	InstallPaths  map[string]string `yaml:"install_paths,omitempty" json:"install_paths,omitempty"`
	FileExtension string            `yaml:"file_extension,omitempty" json:"file_extension,omitempty"`
	RequiresEnv   map[string]string `yaml:"requires_env,omitempty" json:"requires_env,omitempty"`
	Loaders       map[string]string `yaml:"loaders,omitempty" json:"loaders,omitempty"`
	LoaderEnv     string            `yaml:"loader_env,omitempty" json:"loader_env,omitempty"`
	VersionEnv    string            `yaml:"version_env,omitempty" json:"version_env,omitempty"`
	AppID         int               `yaml:"app_id,omitempty" json:"app_id,omitempty"`
}

// ModLoaderConfig describes the mod loader/framework selector shown at the top of the Mods tab.
// This replaces the env var in Settings — the Mods tab owns the loader UX.
type ModLoaderConfig struct {
	EnvKey  string   `yaml:"env_key" json:"env_key"`   // e.g. MODLOADER, OXIDE_ENABLED
	Label   string   `yaml:"label" json:"label"`       // e.g. "Mod Loader", "Oxide (uMod)"
	Type    string   `yaml:"type" json:"type"`          // "select" or "boolean"
	Options []string `yaml:"options,omitempty" json:"options,omitempty"` // for select type
	Default string   `yaml:"default" json:"default"`
}

type ModConfig struct {
	Loader  *ModLoaderConfig  `yaml:"loader,omitempty" json:"loader,omitempty"`
	Sources []ModSourceConfig `yaml:"sources,omitempty" json:"sources,omitempty"`
}

type Assets struct {
	Icon string `yaml:"icon,omitempty"`
}

type GameDefinition struct {
	ID                   string    `yaml:"id"`
	Name                 string    `yaml:"name"`
	Description          string    `yaml:"description,omitempty"`
	BaseImage            string    `yaml:"base_image"`
	RecommendedMemoryMB  int       `yaml:"recommended_memory_mb"`
	GJQSlug              string    `yaml:"gjq_slug,omitempty"`
	ReadyPattern         string    `yaml:"ready_pattern,omitempty"`
	DisabledCapabilities []string  `yaml:"disabled_capabilities"`
	Assets               Assets    `yaml:"assets,omitempty"`
	Ports                []Port    `yaml:"ports"`
	Env                  []EnvVar  `yaml:"env"`
	Mods                 ModConfig `yaml:"mods,omitempty"`
}

// Game is the runtime representation used throughout the application.
type Game struct {
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Description          string    `json:"description,omitempty"`
	BaseImage            string    `json:"base_image"`
	IconPath             string    `json:"icon_path"`
	DefaultPorts         []Port    `json:"default_ports"`
	DefaultEnv           []EnvVar  `json:"default_env"`
	RecommendedMemoryMB  int       `json:"recommended_memory_mb"`
	GJQSlug              string    `json:"gjq_slug,omitempty"`
	ReadyPattern         string    `json:"ready_pattern,omitempty"`
	DisabledCapabilities []string  `json:"disabled_capabilities"`
	Mods                 ModConfig `json:"mods,omitempty"`
}

type GameStore struct {
	games    map[string]*Game
	gameFS   map[string]fs.FS
	sorted   []Game
	log      *slog.Logger
	localDir string
}

func NewGameStore(localGamesDir string, log *slog.Logger) (*GameStore, error) {
	s := &GameStore{
		games:    make(map[string]*Game),
		gameFS:   make(map[string]fs.FS),
		log:      log,
		localDir: localGamesDir,
	}

	// Load embedded games first
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

	// Build sorted list
	s.sorted = make([]Game, 0, len(s.games))
	for _, g := range s.games {
		s.sorted = append(s.sorted, *g)
	}
	sort.Slice(s.sorted, func(i, j int) bool {
		return s.sorted[i].Name < s.sorted[j].Name
	})

	log.Info("game store loaded", "game_count", len(s.games))
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
			s.log.Warn("skipping game directory without game.yaml", "dir", gameDir, "source", source)
			continue
		}

		var def GameDefinition
		if err := yaml.Unmarshal(yamlData, &def); err != nil {
			return fmt.Errorf("parsing game.yaml for %s: %w", gameDir, err)
		}

		if def.ID == "" {
			def.ID = gameDir
		}

		game := definitionToGame(def)

		// Set asset path from YAML definition (empty if no icon configured)
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

func definitionToGame(def GameDefinition) *Game {
	caps := def.DisabledCapabilities
	if caps == nil {
		caps = []string{}
	}

	ports := def.Ports
	if ports == nil {
		ports = []Port{}
	}

	env := def.Env
	if env == nil {
		env = []EnvVar{}
	}

	mods := def.Mods
	if mods.Sources == nil {
		mods.Sources = []ModSourceConfig{}
	}

	return &Game{
		ID:                   def.ID,
		Name:                 def.Name,
		Description:          def.Description,
		BaseImage:            def.BaseImage,
		RecommendedMemoryMB:  def.RecommendedMemoryMB,
		GJQSlug:              def.GJQSlug,
		ReadyPattern:         def.ReadyPattern,
		DefaultPorts:         ports,
		DefaultEnv:           env,
		DisabledCapabilities: caps,
		Mods:                 mods,
	}
}

func (s *GameStore) ListGames() []Game {
	return s.sorted
}

func (s *GameStore) GetGame(id string) *Game {
	return s.games[id]
}

// GetGameFS returns the filesystem for a game's directory (scripts/, assets/, defaults/).
func (s *GameStore) GetGameFS(id string) fs.FS {
	return s.gameFS[id]
}

// AssetsFS returns an fs.FS that serves game assets at {gameID}/{filename}
// for use with the /assets/games/ route. Includes the _default fallback.
func (s *GameStore) AssetsFS() fs.FS {
	return &gameAssetsFS{store: s}
}

// gameAssetsFS implements fs.FS for serving game assets.
type gameAssetsFS struct {
	store *GameStore
}

func (f *gameAssetsFS) Open(name string) (fs.File, error) {
	// name is like "minecraft-java/minecraft-icon.ico" or "_default/default-icon.svg"
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

	// Extract defaults if they exist
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
