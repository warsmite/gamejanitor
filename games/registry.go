package games

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// ── Shared types — importable by gjq, gamejanitorbrowser, gamejanitorhosting ──

// SteamLoginType describes what level of Steam authentication is required to download
// a game's dedicated server files via Steam depot downloader.
type SteamLoginType string

const (
	// SteamLoginAnonymous means no authentication is needed (default for most games).
	SteamLoginAnonymous SteamLoginType = "anonymous"
	// SteamLoginAccount means any authenticated Steam account can download the server files.
	SteamLoginAccount SteamLoginType = "account"
	// SteamLoginOwnership means the Steam account must own the game to download server files.
	SteamLoginOwnership SteamLoginType = "ownership"
)

// RequiresAuth returns true if this game needs a Steam account to download.
func (s SteamLoginType) RequiresAuth() bool {
	return s == SteamLoginAccount || s == SteamLoginOwnership
}

// GameDef is the full YAML representation of a game definition.
// All consumers parse the same YAML; each reads the sections it needs.
type GameDef struct {
	ID          string           `yaml:"id" json:"id"`
	Name        string           `yaml:"name" json:"name"`
	Aliases     []string         `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Description string           `yaml:"description,omitempty" json:"description,omitempty"`
	AppID       uint32           `yaml:"app_id,omitempty" json:"app_id,omitempty"`
	ServerAppID uint32           `yaml:"server_app_id,omitempty" json:"server_app_id,omitempty"`
	SteamLogin  SteamLoginType   `yaml:"steam_login,omitempty" json:"steam_login,omitempty"`
	Ports       []Port           `yaml:"ports" json:"ports"`
	Query       *QueryConfig     `yaml:"query,omitempty" json:"query,omitempty"`
	Instance    *InstanceConfig `yaml:"instance,omitempty" json:"instance,omitempty"`
	Assets      Assets           `yaml:"assets,omitempty" json:"assets,omitempty"`
}

// QueryConfig describes how to query a game server's status.
// Used by gjq for protocol selection and gamejanitor for polling.
type QueryConfig struct {
	Protocol string          `yaml:"protocol" json:"protocol"`
	Supports []string        `yaml:"supports,omitempty" json:"supports,omitempty"`
	Notes    string          `yaml:"notes,omitempty" json:"notes,omitempty"`
	EOS      *EOSQueryConfig `yaml:"eos,omitempty" json:"eos,omitempty"`
}

// EOSQueryConfig holds Epic Online Services credentials for game server queries.
// These are public credentials shipped in game binaries — not secrets.
type EOSQueryConfig struct {
	ClientID        string            `yaml:"client_id" json:"client_id"`
	ClientSecret    string            `yaml:"client_secret" json:"client_secret"`
	DeploymentID    string            `yaml:"deployment_id" json:"deployment_id"`
	UseExternalAuth bool              `yaml:"use_external_auth,omitempty" json:"use_external_auth,omitempty"`
	UseWildcard     bool              `yaml:"use_wildcard,omitempty" json:"use_wildcard,omitempty"`
	Attributes      map[string]string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
}

// InstanceConfig holds fields only needed by gamejanitor for running game servers.
// Query-only games (most gjq games) omit this section entirely.
type InstanceConfig struct {
	Image                string         `yaml:"image" json:"image"`
	Runtime              *RuntimeConfig `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	ReadyPattern         string         `yaml:"ready_pattern,omitempty" json:"ready_pattern,omitempty"`
	RecommendedMemoryMB  int            `yaml:"recommended_memory_mb,omitempty" json:"recommended_memory_mb,omitempty"`
	DisabledCapabilities []string       `yaml:"disabled_capabilities,omitempty" json:"disabled_capabilities,omitempty"`
	Env                  []EnvVar       `yaml:"env,omitempty" json:"env,omitempty"`
	Mods                 ModsConfig     `yaml:"mods,omitempty" json:"mods,omitempty"`
}

// HasCapability returns true if the capability is NOT disabled.
// Returns true if Instance is nil (query-only games have no disabled capabilities).
func (d *GameDef) HasCapability(capability string) bool {
	if d.Instance == nil {
		return true
	}
	for _, cap := range d.Instance.DisabledCapabilities {
		if cap == capability {
			return false
		}
	}
	return true
}

// GamePort returns the default game port, or 0 if none defined.
func (d *GameDef) GamePort() uint16 {
	for _, p := range d.Ports {
		if p.Name == "game" {
			return uint16(p.Port)
		}
	}
	if len(d.Ports) > 0 {
		return uint16(d.Ports[0].Port)
	}
	return 0
}

// QueryPort returns the default query port. Falls back to game port if no
// explicit query port is defined (common for Source engine games).
func (d *GameDef) QueryPort() uint16 {
	for _, p := range d.Ports {
		if p.Name == "query" {
			return uint16(p.Port)
		}
	}
	return d.GamePort()
}

// HasQuery returns true if this game has query protocol support.
func (d *GameDef) HasQuery() bool {
	return d.Query != nil && d.Query.Protocol != ""
}

// HasInstance returns true if this game has instance support.
func (d *GameDef) HasInstance() bool {
	return d.Instance != nil && d.Instance.Image != ""
}

// ── Registry — shared game lookup used by all consumers ──

// Registry holds all parsed game definitions and provides lookups.
// It loads from an embedded filesystem and is safe for concurrent reads.
type Registry struct {
	games          map[string]*GameDef
	aliases        map[string]string
	sorted         []GameDef
	queryPortIndex map[uint16][]*GameDef
	gamePortIndex  map[uint16][]*GameDef
}

// NewRegistry loads all game definitions from the embedded data/ directory.
func NewRegistry() (*Registry, error) {
	root, err := fs.Sub(embeddedGames, "data")
	if err != nil {
		return nil, fmt.Errorf("accessing embedded game data: %w", err)
	}
	return newRegistryFromFS(root)
}

func newRegistryFromFS(root fs.FS) (*Registry, error) {
	r := &Registry{
		games:          make(map[string]*GameDef),
		aliases:        make(map[string]string),
		queryPortIndex: make(map[uint16][]*GameDef),
		gamePortIndex:  make(map[uint16][]*GameDef),
	}

	entries, err := fs.ReadDir(root, ".")
	if err != nil {
		return nil, fmt.Errorf("reading game directories: %w", err)
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
			return nil, fmt.Errorf("parsing game.yaml for %s: %w", gameDir, err)
		}

		if def.ID == "" {
			def.ID = gameDir
		}

		// Normalize nil slices
		if def.Ports == nil {
			def.Ports = []Port{}
		}
		if def.Aliases == nil {
			def.Aliases = []string{}
		}
		if def.Instance != nil {
			if def.Instance.DisabledCapabilities == nil {
				def.Instance.DisabledCapabilities = []string{}
			}
			if def.Instance.Env == nil {
				def.Instance.Env = []EnvVar{}
			}
			if def.Instance.Mods.Categories == nil {
				def.Instance.Mods.Categories = []ModCategoryDef{}
			}
		}
		if def.Query != nil && def.Query.Supports == nil {
			def.Query.Supports = []string{}
		}

		r.games[def.ID] = &def
	}

	// Build alias map, port indexes, and sorted list
	r.sorted = make([]GameDef, 0, len(r.games))
	for _, g := range r.games {
		r.sorted = append(r.sorted, *g)
		for _, alias := range g.Aliases {
			r.aliases[alias] = g.ID
		}

		gamePort := g.GamePort()
		queryPort := g.QueryPort()
		if gamePort > 0 {
			r.gamePortIndex[gamePort] = append(r.gamePortIndex[gamePort], g)
		}
		if queryPort > 0 {
			r.queryPortIndex[queryPort] = append(r.queryPortIndex[queryPort], g)
		}
	}
	sort.Slice(r.sorted, func(i, j int) bool {
		return r.sorted[i].Name < r.sorted[j].Name
	})

	return r, nil
}

// Get returns a game by ID or alias. Returns nil if not found.
func (r *Registry) Get(id string) *GameDef {
	if g, ok := r.games[id]; ok {
		return g
	}
	if realID, ok := r.aliases[id]; ok {
		return r.games[realID]
	}
	return nil
}

// Resolve returns the canonical game ID for an ID or alias.
// Returns the input unchanged if not found.
func (r *Registry) Resolve(id string) string {
	if _, ok := r.games[id]; ok {
		return id
	}
	if realID, ok := r.aliases[id]; ok {
		return realID
	}
	return id
}

// List returns all game definitions sorted by name.
func (r *Registry) List() []GameDef {
	return r.sorted
}

// WithQuery returns games that have query protocol support.
func (r *Registry) WithQuery() []GameDef {
	var result []GameDef
	for _, g := range r.sorted {
		if g.HasQuery() {
			result = append(result, g)
		}
	}
	return result
}

// WithInstance returns games that have instance support.
func (r *Registry) WithInstance() []GameDef {
	var result []GameDef
	for _, g := range r.sorted {
		if g.HasInstance() {
			result = append(result, g)
		}
	}
	return result
}

// ByAppID returns a game by Steam AppID. Returns nil if not found.
func (r *Registry) ByAppID(appID uint32) *GameDef {
	if appID == 0 {
		return nil
	}
	for _, g := range r.games {
		if g.AppID == appID {
			return g
		}
	}
	return nil
}

// ByGamePort returns games that use the given port as their default game port.
func (r *Registry) ByGamePort(port uint16) []*GameDef {
	return r.gamePortIndex[port]
}

// ByQueryPort returns games that use the given port as their default query port.
func (r *Registry) ByQueryPort(port uint16) []*GameDef {
	return r.queryPortIndex[port]
}

// Count returns the total number of games in the registry.
func (r *Registry) Count() int {
	return len(r.games)
}
