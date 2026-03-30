package games

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// OptionsProvider supplies dynamic option lists for env var selects.
// Implementations fetch and cache options from external sources.
type OptionsProvider interface {
	// Options returns the available options. May be cached.
	Options() ([]Option, error)
}

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"` // for grouping in the UI (e.g. "releases", "snapshots")
}

// OptionsRegistry maps provider source names to their implementations.
type OptionsRegistry struct {
	providers map[string]OptionsProvider
}

func NewOptionsRegistry(log *slog.Logger) *OptionsRegistry {
	r := &OptionsRegistry{
		providers: make(map[string]OptionsProvider),
	}

	// Register built-in providers
	r.providers["mojang-versions"] = newMojangVersionsProvider(log)

	return r
}

func (r *OptionsRegistry) Get(source string) (OptionsProvider, bool) {
	p, ok := r.providers[source]
	return p, ok
}

// ResolveValue resolves special values like "latest" for a dynamic options source.
// Returns the input unchanged if the source doesn't exist or the value isn't resolvable.
func (r *OptionsRegistry) ResolveValue(source string, value string) string {
	p, ok := r.providers[source]
	if !ok {
		return value
	}
	if rv, ok := p.(ValueResolver); ok {
		if resolved := rv.ResolveValue(value); resolved != "" {
			return resolved
		}
	}
	return value
}

// ValueResolver is optionally implemented by providers that can resolve alias values.
type ValueResolver interface {
	ResolveValue(value string) string
}

// GetOptionsForEnv resolves dynamic options for an env var, if applicable.
// Returns nil if the env var uses static options or has no dynamic_options.
func (r *OptionsRegistry) GetOptionsForEnv(env EnvVar) ([]Option, error) {
	if env.DynamicOptions == nil {
		return nil, nil
	}
	p, ok := r.Get(env.DynamicOptions.Source)
	if !ok {
		return nil, fmt.Errorf("unknown options provider: %s", env.DynamicOptions.Source)
	}
	return p.Options()
}

// ── Mojang Versions Provider ──

type mojangVersionsProvider struct {
	log     *slog.Logger
	client  *http.Client
	mu      sync.RWMutex
	cached  []Option
	fetched time.Time
}

func newMojangVersionsProvider(log *slog.Logger) *mojangVersionsProvider {
	return &mojangVersionsProvider{
		log:    log,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Mojang version manifest response
type mojangManifest struct {
	Latest   mojangLatest    `json:"latest"`
	Versions []mojangVersion `json:"versions"`
}

type mojangLatest struct {
	Release  string `json:"release"`
	Snapshot string `json:"snapshot"`
}

type mojangVersion struct {
	ID   string `json:"id"`
	Type string `json:"type"` // "release" or "snapshot"
	URL  string `json:"url"`
}

const (
	mojangManifestURL = "https://launchermeta.mojang.com/mc/game/version_manifest.json"
	mojangCacheTTL    = 30 * time.Minute
)

// ResolveValue resolves "latest" to the actual latest release version.
func (p *mojangVersionsProvider) ResolveValue(value string) string {
	if value != "latest" {
		return ""
	}
	// Fetch/use cached manifest to get the latest release
	opts, err := p.Options()
	if err != nil || len(opts) == 0 {
		return ""
	}
	// The "latest" option's label is "Latest (1.21.11)" — parse from the first release option instead
	for _, o := range opts {
		if o.Group == "releases" {
			return o.Value
		}
	}
	return ""
}

func (p *mojangVersionsProvider) Options() ([]Option, error) {
	p.mu.RLock()
	if p.cached != nil && time.Since(p.fetched) < mojangCacheTTL {
		defer p.mu.RUnlock()
		return p.cached, nil
	}
	p.mu.RUnlock()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring write lock
	if p.cached != nil && time.Since(p.fetched) < mojangCacheTTL {
		return p.cached, nil
	}

	resp, err := p.client.Get(mojangManifestURL)
	if err != nil {
		// Return stale cache if available
		if p.cached != nil {
			p.log.Warn("failed to fetch Mojang versions, using stale cache", "error", err)
			return p.cached, nil
		}
		return nil, fmt.Errorf("fetching Mojang version manifest: %w", err)
	}
	defer resp.Body.Close()

	var manifest mojangManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		if p.cached != nil {
			p.log.Warn("failed to parse Mojang versions, using stale cache", "error", err)
			return p.cached, nil
		}
		return nil, fmt.Errorf("parsing Mojang version manifest: %w", err)
	}

	var options []Option

	// "latest" as the first option
	options = append(options, Option{
		Value: "latest",
		Label: fmt.Sprintf("Latest (%s)", manifest.Latest.Release),
		Group: "latest",
	})

	// Releases and snapshots — only include versions with a dedicated server jar.
	// Server JARs are available from 1.2.5 onwards (April 2012). The manifest
	// lists versions in reverse chronological order, so we stop at 1.2.4.
	stopRelease := false
	for _, v := range manifest.Versions {
		if v.ID == "1.2.4" {
			stopRelease = true
		}
		if stopRelease {
			continue
		}
		switch v.Type {
		case "release":
			options = append(options, Option{Value: v.ID, Label: v.ID, Group: "releases"})
		case "snapshot":
			options = append(options, Option{Value: v.ID, Label: v.ID, Group: "snapshots"})
		}
	}

	p.cached = options
	p.fetched = time.Now()
	p.log.Info("fetched Mojang versions", "releases", len(options))

	return options, nil
}
