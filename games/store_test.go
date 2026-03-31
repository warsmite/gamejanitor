package games

import (
	"log/slog"
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestGameStore_LoadsAllGames(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	games := store.ListGames()
	assert.GreaterOrEqual(t, len(games), 10, "should load at least 10 instance games")
}

func TestGameStore_GetGame_ReturnsCorrectFields(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	game := store.GetGame("minecraft-java")
	require.NotNil(t, game, "minecraft-java should exist")
	assert.Equal(t, "minecraft-java", game.ID)
	assert.NotEmpty(t, game.Name)
	assert.NotEmpty(t, game.BaseImage)
	assert.NotEmpty(t, game.DefaultPorts)
}

func TestGameStore_GetGame_NotFound(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	game := store.GetGame("nonexistent-game")
	assert.Nil(t, game)
}

func TestGameStore_AllGames_HaveRequiredFields(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	for _, game := range store.ListGames() {
		t.Run(game.ID, func(t *testing.T) {
			assert.NotEmpty(t, game.ID, "game must have an ID")
			assert.NotEmpty(t, game.Name, "game must have a name")
			assert.NotEmpty(t, game.BaseImage, "game must have a base_image")
			assert.NotEmpty(t, game.DefaultPorts, "game must have at least one port")
		})
	}
}

func TestGameStore_AllGames_ReadyPatternsCompile(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	for _, game := range store.ListGames() {
		if game.ReadyPattern == "" {
			continue
		}
		t.Run(game.ID, func(t *testing.T) {
			_, err := regexp.Compile(game.ReadyPattern)
			assert.NoError(t, err, "ready_pattern should be a valid regex")
		})
	}
}

func TestGameStore_AllGames_NoDuplicatePortsWithinGame(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	for _, game := range store.ListGames() {
		t.Run(game.ID, func(t *testing.T) {
			type portKey struct {
				Port     int
				Protocol string
			}
			seen := make(map[portKey]bool)
			for _, p := range game.DefaultPorts {
				key := portKey{p.Port, p.Protocol}
				assert.False(t, seen[key], "duplicate port %d/%s in game %s", p.Port, p.Protocol, game.ID)
				seen[key] = true
			}
		})
	}
}

func TestGameStore_AllGames_ValidEnvTypes(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	validTypes := map[string]bool{"": true, "text": true, "number": true, "boolean": true, "select": true}

	for _, game := range store.ListGames() {
		for _, env := range game.DefaultEnv {
			t.Run(game.ID+"/"+env.Key, func(t *testing.T) {
				assert.True(t, validTypes[env.Type], "env var %s has invalid type %q", env.Key, env.Type)
			})
		}
	}
}

func TestGameStore_AllGames_SelectEnvHaveOptions(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	for _, game := range store.ListGames() {
		for _, env := range game.DefaultEnv {
			if env.Type != "select" {
				continue
			}
			t.Run(game.ID+"/"+env.Key, func(t *testing.T) {
				hasOptions := len(env.Options) > 0 || env.DynamicOptions != nil
				assert.True(t, hasOptions, "select env var %s must have options or dynamic_options", env.Key)
			})
		}
	}
}

func TestGameStore_LocalOverride(t *testing.T) {
	t.Parallel()

	// Create a temp dir with a custom game using the new format
	dir := t.TempDir()
	gameDir := dir + "/custom-game"
	os.MkdirAll(gameDir, 0755)
	os.WriteFile(gameDir+"/game.yaml", []byte(`
id: custom-game
name: "Custom Game"
ports:
  - name: game
    port: 9999
    protocol: tcp
instance:
  image: alpine:latest
`), 0644)

	store, err := NewGameStore(dir, testLogger())
	require.NoError(t, err)

	game := store.GetGame("custom-game")
	require.NotNil(t, game, "custom game should be loaded")
	assert.Equal(t, "Custom Game", game.Name)
	assert.Equal(t, "alpine:latest", game.BaseImage)
}

func TestGameStore_QueryOnlyGamesNotInStore(t *testing.T) {
	t.Parallel()
	store, err := NewGameStore("", testLogger())
	require.NoError(t, err)

	// dayz is query-only (no instance section) — should not appear in GameStore
	game := store.GetGame("dayz")
	assert.Nil(t, game, "query-only games should not be in GameStore")
}

func TestRegistry_LoadsAllGames(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	all := registry.List()
	assert.GreaterOrEqual(t, len(all), 80, "registry should load 80+ games (instance + query-only)")
}

func TestRegistry_WithQuery(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	queryGames := registry.WithQuery()
	assert.GreaterOrEqual(t, len(queryGames), 70, "should have 70+ queryable games")

	for _, g := range queryGames {
		assert.True(t, g.HasQuery(), "WithQuery should only return games with query config")
	}
}

func TestRegistry_WithInstance(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	instanceGames := registry.WithInstance()
	assert.GreaterOrEqual(t, len(instanceGames), 10, "should have 10+ instance games")

	for _, g := range instanceGames {
		assert.True(t, g.HasInstance(), "WithInstance should only return games with instance config")
	}
}

func TestRegistry_Get_ByAlias(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	game := registry.Get("mc")
	require.NotNil(t, game, "should resolve 'mc' alias")
	assert.Equal(t, "minecraft-java", game.ID)
}

func TestRegistry_ByAppID(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	game := registry.ByAppID(252490)
	require.NotNil(t, game, "should find Rust by AppID")
	assert.Equal(t, "rust", game.ID)
}

func TestRegistry_PortIndexes(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	// Rust query port is 28017
	games := registry.ByQueryPort(28017)
	assert.NotEmpty(t, games, "should find games with query port 28017")
}

func TestRegistry_AllGames_ValidYAML(t *testing.T) {
	t.Parallel()
	registry, err := NewRegistry()
	require.NoError(t, err)

	for _, g := range registry.List() {
		t.Run(g.ID, func(t *testing.T) {
			assert.NotEmpty(t, g.ID, "game must have an ID")
			assert.NotEmpty(t, g.Name, "game must have a name")
			assert.NotEmpty(t, g.Ports, "game must have at least one port")

			// Every game must have either query or instance support (or both)
			assert.True(t, g.HasQuery() || g.HasInstance(),
				"game must have query and/or instance support")
		})
	}
}
