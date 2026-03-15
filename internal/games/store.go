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
	Key          string   `yaml:"key" json:"key"`
	Default      string   `yaml:"default" json:"default"`
	Label        string   `yaml:"label,omitempty" json:"label,omitempty"`
	Type         string   `yaml:"type,omitempty" json:"type,omitempty"`
	Options      []string `yaml:"options,omitempty" json:"options,omitempty"`
	Required     bool     `yaml:"required,omitempty" json:"required,omitempty"`
	Notice       string   `yaml:"notice,omitempty" json:"notice,omitempty"`
	Autogenerate string   `yaml:"autogenerate,omitempty" json:"autogenerate,omitempty"`
	System       bool     `yaml:"system,omitempty" json:"system,omitempty"`
}

type Assets struct {
	Icon string `yaml:"icon,omitempty"`
	Grid string `yaml:"grid,omitempty"`
	Hero string `yaml:"hero,omitempty"`
}

type GameDefinition struct {
	ID                   string   `yaml:"id"`
	Name                 string   `yaml:"name"`
	BaseImage            string   `yaml:"base_image"`
	RecommendedMemoryMB  int      `yaml:"recommended_memory_mb"`
	GSQSlug              string   `yaml:"gsq_slug,omitempty"`
	ReadyPattern         string   `yaml:"ready_pattern,omitempty"`
	ReadyTimeout         int      `yaml:"ready_timeout,omitempty"`
	DisabledCapabilities []string `yaml:"disabled_capabilities"`
	Assets               Assets   `yaml:"assets,omitempty"`
	Ports                []Port   `yaml:"ports"`
	Env                  []EnvVar `yaml:"env"`
}

// Game is the runtime representation used throughout the application.
type Game struct {
	ID                   string   `json:"id"`
	Name                 string   `json:"name"`
	BaseImage            string   `json:"base_image"`
	IconPath             string   `json:"icon_path"`
	GridPath             string   `json:"grid_path"`
	HeroPath             string   `json:"hero_path"`
	DefaultPorts         []Port   `json:"default_ports"`
	DefaultEnv           []EnvVar `json:"default_env"`
	RecommendedMemoryMB  int      `json:"recommended_memory_mb"`
	GSQSlug              string   `json:"gsq_slug,omitempty"`
	ReadyPattern         string   `json:"ready_pattern,omitempty"`
	ReadyTimeout         int      `json:"ready_timeout,omitempty"`
	DisabledCapabilities []string `json:"disabled_capabilities"`
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

		// Set asset paths from YAML definition
		assetPrefix := "/static/games/" + def.ID + "/"
		defaultAsset := "/static/games/default/"
		if def.Assets.Icon != "" {
			game.IconPath = assetPrefix + def.Assets.Icon
		} else {
			game.IconPath = defaultAsset + "default-icon.svg"
		}
		if def.Assets.Grid != "" {
			game.GridPath = assetPrefix + def.Assets.Grid
		} else {
			game.GridPath = defaultAsset + "default-grid.svg"
		}
		if def.Assets.Hero != "" {
			game.HeroPath = assetPrefix + def.Assets.Hero
		} else {
			game.HeroPath = defaultAsset + "default-hero.svg"
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

	readyTimeout := def.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = 300
	}

	return &Game{
		ID:                   def.ID,
		Name:                 def.Name,
		BaseImage:            def.BaseImage,
		RecommendedMemoryMB:  def.RecommendedMemoryMB,
		GSQSlug:              def.GSQSlug,
		ReadyPattern:         def.ReadyPattern,
		ReadyTimeout:         readyTimeout,
		DefaultPorts:         ports,
		DefaultEnv:           env,
		DisabledCapabilities: caps,
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
// for use with the /static/games/ route. Includes the _default fallback.
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
