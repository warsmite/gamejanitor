package games

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// ResolveImage returns the instance image for a game given the current env.
// For games with a RuntimeConfig resolver, it dynamically selects the image.
// For games with a static image, it returns BaseImage directly.
func (g *Game) ResolveImage(env map[string]string) string {
	if g.Runtime.Resolver == "" {
		// No resolver — use the static base image
		if g.Runtime.Image != "" {
			return g.Runtime.Image
		}
		return g.BaseImage
	}

	resolver, ok := imageResolvers[g.Runtime.Resolver]
	if !ok {
		// Unknown resolver — fall back
		if g.Runtime.DefaultImage != "" {
			return g.Runtime.DefaultImage
		}
		return g.BaseImage
	}

	imageKey, err := resolver(env)
	if err != nil || imageKey == "" {
		if g.Runtime.DefaultImage != "" {
			return g.Runtime.DefaultImage
		}
		return g.BaseImage
	}

	if img, ok := g.Runtime.Images[imageKey]; ok {
		return img
	}

	if g.Runtime.DefaultImage != "" {
		return g.Runtime.DefaultImage
	}
	return g.BaseImage
}

// imageResolvers maps resolver names to functions that return an image key
// (e.g., "java21") given the gameserver's env vars.
var imageResolvers = map[string]func(env map[string]string) (string, error){
	"minecraft-java": resolveMinecraftJava,
}

// --- Minecraft Java resolver ---
//
// Fetches the per-version Mojang manifest to read javaVersion.majorVersion.
// Results are cached forever per version (Java version for a MC version never changes).

var (
	javaVersionCache   = make(map[string]int)
	javaVersionCacheMu sync.RWMutex
	javaResolveClient  = &http.Client{Timeout: 10 * time.Second}
)

func resolveMinecraftJava(env map[string]string) (string, error) {
	mcVersion := env["MINECRAFT_VERSION"]
	if mcVersion == "" {
		return "", nil // fall back to default image
	}

	// Resolve "latest" / "latest-snapshot" to actual version number
	if mcVersion == "latest" || mcVersion == "latest-snapshot" {
		manifest, err := getMojangMainManifest()
		if err != nil {
			return "", nil // fall back to default
		}
		if mcVersion == "latest-snapshot" {
			mcVersion = manifest.Latest.Snapshot
		} else {
			mcVersion = manifest.Latest.Release
		}
		if mcVersion == "" {
			return "", nil
		}
	}

	// Check cache first
	javaVersionCacheMu.RLock()
	if jv, ok := javaVersionCache[mcVersion]; ok {
		javaVersionCacheMu.RUnlock()
		return "java" + strconv.Itoa(jv), nil
	}
	javaVersionCacheMu.RUnlock()

	// Fetch the Mojang version manifest to find this version's URL
	versionURL, err := lookupMojangVersionURL(mcVersion)
	if err != nil {
		return "", err
	}

	// Fetch the per-version manifest to get javaVersion.majorVersion
	javaVersion, err := fetchJavaVersion(versionURL)
	if err != nil {
		return "", err
	}

	// Cache forever
	javaVersionCacheMu.Lock()
	javaVersionCache[mcVersion] = javaVersion
	javaVersionCacheMu.Unlock()

	return "java" + strconv.Itoa(javaVersion), nil
}

// mojangMainManifest is cached to avoid refetching the version list on every resolution.
var (
	mojangMainManifestCache *mojangMainManifest
	mojangMainManifestMu    sync.RWMutex
	mojangMainManifestFetch time.Time
)

type mojangMainManifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		URL  string `json:"url"`
	} `json:"versions"`
}

func lookupMojangVersionURL(version string) (string, error) {
	manifest, err := getMojangMainManifest()
	if err != nil {
		return "", err
	}

	for _, v := range manifest.Versions {
		if v.ID == version {
			return v.URL, nil
		}
	}
	return "", fmt.Errorf("minecraft version %q not found in Mojang manifest", version)
}

func getMojangMainManifest() (*mojangMainManifest, error) {
	mojangMainManifestMu.RLock()
	if mojangMainManifestCache != nil && time.Since(mojangMainManifestFetch) < 30*time.Minute {
		defer mojangMainManifestMu.RUnlock()
		return mojangMainManifestCache, nil
	}
	mojangMainManifestMu.RUnlock()

	mojangMainManifestMu.Lock()
	defer mojangMainManifestMu.Unlock()

	// Double-check after acquiring write lock
	if mojangMainManifestCache != nil && time.Since(mojangMainManifestFetch) < 30*time.Minute {
		return mojangMainManifestCache, nil
	}

	resp, err := javaResolveClient.Get("https://launchermeta.mojang.com/mc/game/version_manifest.json")
	if err != nil {
		if mojangMainManifestCache != nil {
			return mojangMainManifestCache, nil // stale cache is better than nothing
		}
		return nil, fmt.Errorf("fetching Mojang version manifest: %w", err)
	}
	defer resp.Body.Close()

	var manifest mojangMainManifest
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		if mojangMainManifestCache != nil {
			return mojangMainManifestCache, nil
		}
		return nil, fmt.Errorf("decoding Mojang version manifest: %w", err)
	}

	mojangMainManifestCache = &manifest
	mojangMainManifestFetch = time.Now()
	return &manifest, nil
}

type mojangVersionDetail struct {
	JavaVersion struct {
		MajorVersion int `json:"majorVersion"`
	} `json:"javaVersion"`
}

func fetchJavaVersion(versionURL string) (int, error) {
	resp, err := javaResolveClient.Get(versionURL)
	if err != nil {
		return 0, fmt.Errorf("fetching Mojang version detail: %w", err)
	}
	defer resp.Body.Close()

	var detail mojangVersionDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return 0, fmt.Errorf("decoding Mojang version detail: %w", err)
	}

	if detail.JavaVersion.MajorVersion == 0 {
		return 8, nil // very old versions don't have javaVersion field, default to Java 8
	}
	return detail.JavaVersion.MajorVersion, nil
}
