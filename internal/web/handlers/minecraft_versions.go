package handlers

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

const (
	mojangManifestURL = "https://launchermeta.mojang.com/mc/game/version_manifest_v2.json"
	manifestCacheTTL  = 1 * time.Hour
	// 1.2.5 is the first release with a server download
	minReleaseVersion = "1.2.5"
)

type MinecraftVersionsHandler struct {
	log   *slog.Logger
	mu    sync.RWMutex
	cache *minecraftVersionCache
}

type minecraftVersionCache struct {
	fetchedAt      time.Time
	latestRelease  string
	latestSnapshot string
	// allVersions preserves Mojang's manifest order (latest-first, interleaved)
	allVersions []minecraftVersion
}

type minecraftVersion struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type mojangManifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"versions"`
}

func NewMinecraftVersionsHandler(log *slog.Logger) *MinecraftVersionsHandler {
	return &MinecraftVersionsHandler{log: log}
}

func (h *MinecraftVersionsHandler) List(w http.ResponseWriter, r *http.Request) {
	includeSnapshots := r.URL.Query().Get("include_snapshots") == "true"

	cache, err := h.getCachedManifest()
	if err != nil {
		h.log.Error("fetching minecraft version manifest", "error", err)
		respondError(w, http.StatusBadGateway, "failed to fetch Minecraft versions")
		return
	}

	var versions []minecraftVersion
	if includeSnapshots {
		versions = cache.allVersions
	} else {
		versions = make([]minecraftVersion, 0, len(cache.allVersions)/2)
		for _, v := range cache.allVersions {
			if v.Type == "release" {
				versions = append(versions, v)
			}
		}
	}

	respondOK(w, struct {
		LatestRelease  string             `json:"latest_release"`
		LatestSnapshot string             `json:"latest_snapshot"`
		Versions       []minecraftVersion `json:"versions"`
	}{
		LatestRelease:  cache.latestRelease,
		LatestSnapshot: cache.latestSnapshot,
		Versions:       versions,
	})
}

func (h *MinecraftVersionsHandler) getCachedManifest() (*minecraftVersionCache, error) {
	h.mu.RLock()
	if h.cache != nil && time.Since(h.cache.fetchedAt) < manifestCacheTTL {
		c := h.cache
		h.mu.RUnlock()
		return c, nil
	}
	h.mu.RUnlock()

	h.mu.Lock()
	defer h.mu.Unlock()

	// Double-check after acquiring write lock
	if h.cache != nil && time.Since(h.cache.fetchedAt) < manifestCacheTTL {
		return h.cache, nil
	}

	cache, err := h.fetchManifest()
	if err != nil {
		return nil, err
	}
	h.cache = cache
	return cache, nil
}

func (h *MinecraftVersionsHandler) fetchManifest() (*minecraftVersionCache, error) {
	h.log.Info("fetching minecraft version manifest from Mojang")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(mojangManifestURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, err
	}

	var manifest mojangManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, err
	}

	cache := &minecraftVersionCache{
		fetchedAt:      time.Now(),
		latestRelease:  manifest.Latest.Release,
		latestSnapshot: manifest.Latest.Snapshot,
	}

	// Manifest is already sorted latest-first.
	// Include releases from latest down to 1.2.5, and all snapshots.
	pastMinRelease := false
	for _, v := range manifest.Versions {
		if v.Type == "release" {
			if pastMinRelease {
				continue
			}
			cache.allVersions = append(cache.allVersions, minecraftVersion{ID: v.ID, Type: v.Type})
			if v.ID == minReleaseVersion {
				pastMinRelease = true
			}
		} else if v.Type == "snapshot" {
			cache.allVersions = append(cache.allVersions, minecraftVersion{ID: v.ID, Type: v.Type})
		}
	}

	h.log.Info("cached minecraft versions",
		"total", len(cache.allVersions),
		"latest_release", cache.latestRelease,
	)

	return cache, nil
}
