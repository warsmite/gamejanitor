package games

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestResolveImage_StaticImage(t *testing.T) {
	t.Parallel()
	g := &Game{
		BaseImage: "ghcr.io/warsmite/gamejanitor/steamcmd",
	}
	assert.Equal(t, "ghcr.io/warsmite/gamejanitor/steamcmd", g.ResolveImage(nil))
}

func TestResolveImage_RuntimeStaticOverride(t *testing.T) {
	t.Parallel()
	g := &Game{
		BaseImage: "old-image",
		Runtime: RuntimeConfig{
			Image: "new-image",
		},
	}
	assert.Equal(t, "new-image", g.ResolveImage(nil))
}

func TestResolveImage_UnknownResolver_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	g := &Game{
		BaseImage: "base",
		Runtime: RuntimeConfig{
			Resolver:     "nonexistent-resolver",
			DefaultImage: "default-fallback",
		},
	}
	assert.Equal(t, "default-fallback", g.ResolveImage(map[string]string{}))
}

func TestResolveImage_UnknownResolver_FallsBackToBase(t *testing.T) {
	t.Parallel()
	g := &Game{
		BaseImage: "base",
		Runtime: RuntimeConfig{
			Resolver: "nonexistent-resolver",
		},
	}
	assert.Equal(t, "base", g.ResolveImage(map[string]string{}))
}

func TestResolveImage_MinecraftResolver_EmptyVersion_FallsBack(t *testing.T) {
	t.Parallel()
	g := &Game{
		BaseImage: "base",
		Runtime: RuntimeConfig{
			Resolver:     "minecraft-java",
			DefaultImage: "default-java",
			Images: map[string]string{
				"java21": "ghcr.io/java21",
			},
		},
	}
	// Empty version → resolver returns "", falls back to default
	assert.Equal(t, "default-java", g.ResolveImage(map[string]string{}))

	// "latest" also falls back (needs resolution by options registry first)
	assert.Equal(t, "default-java", g.ResolveImage(map[string]string{"MINECRAFT_VERSION": "latest"}))
}

func TestResolveImage_MinecraftResolver_CachedVersion(t *testing.T) {
	t.Parallel()

	// Pre-populate the cache to avoid HTTP calls in tests
	javaVersionCacheMu.Lock()
	javaVersionCache["1.21.1"] = 21
	javaVersionCache["1.16.5"] = 8
	javaVersionCacheMu.Unlock()

	g := &Game{
		BaseImage: "base",
		Runtime: RuntimeConfig{
			Resolver:     "minecraft-java",
			DefaultImage: "default-java",
			Images: map[string]string{
				"java8":  "ghcr.io/java8",
				"java17": "ghcr.io/java17",
				"java21": "ghcr.io/java21",
			},
		},
	}

	assert.Equal(t, "ghcr.io/java21", g.ResolveImage(map[string]string{"MINECRAFT_VERSION": "1.21.1"}))
	assert.Equal(t, "ghcr.io/java8", g.ResolveImage(map[string]string{"MINECRAFT_VERSION": "1.16.5"}))
}

func TestResolveImage_MinecraftResolver_UnknownImageKey_FallsBack(t *testing.T) {
	t.Parallel()

	javaVersionCacheMu.Lock()
	javaVersionCache["1.99.0"] = 99
	javaVersionCacheMu.Unlock()

	g := &Game{
		BaseImage: "base",
		Runtime: RuntimeConfig{
			Resolver:     "minecraft-java",
			DefaultImage: "default-java",
			Images: map[string]string{
				"java21": "ghcr.io/java21",
			},
		},
	}
	// java99 not in Images map → falls back to default
	assert.Equal(t, "default-java", g.ResolveImage(map[string]string{"MINECRAFT_VERSION": "1.99.0"}))
}
