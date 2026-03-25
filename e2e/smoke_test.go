//go:build smoke

package e2e

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/games"
)

// TestSmoke runs a full lifecycle against a real game.
// Parameterized by SMOKE_GAME (default: terraria) or SMOKE_GAMES (comma-separated).
func TestSmoke(t *testing.T) {
	gameIDs := smokeGames(t)

	for _, gameID := range gameIDs {
		t.Run(gameID, func(t *testing.T) {
			runSmokeTest(t, gameID)
		})
	}
}

func smokeGames(t *testing.T) []string {
	t.Helper()

	if v := os.Getenv("SMOKE_GAMES"); v != "" {
		if v == "all" {
			store, err := games.NewGameStore("", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
			if err != nil {
				t.Fatalf("loading game store: %v", err)
			}
			var ids []string
			for _, g := range store.ListGames() {
				ids = append(ids, g.ID)
			}
			return ids
		}
		return strings.Split(v, ",")
	}

	if v := os.Getenv("SMOKE_GAME"); v != "" {
		return []string{v}
	}

	return []string{"terraria"}
}

func runSmokeTest(t *testing.T, gameID string) {
	// Load the game definition to understand its requirements
	store, err := games.NewGameStore("", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	require.NoError(t, err)

	game := store.GetGame(gameID)
	if game == nil {
		t.Skipf("game %q not found in game store", gameID)
	}

	installTimeout := envDuration("SMOKE_INSTALL_TIMEOUT", 5*time.Minute)
	readyTimeout := envDuration("SMOKE_READY_TIMEOUT", 2*time.Minute)

	// Build env vars from game defaults
	env := buildEnvFromDefaults(t, game)
	if env == nil {
		return // test was skipped due to unfillable required vars
	}

	t.Logf("smoke testing %s (image: %s, ready_pattern: %q)", gameID, game.BaseImage, game.ReadyPattern)

	h := Start(t)

	// Create
	resp, err := h.PostJSON("/api/gameservers", map[string]any{
		"name":    "smoke-" + gameID,
		"game_id": gameID,
		"env":     env,
	})
	require.NoError(t, err)
	var gs struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	require.NoError(t, DecodeData(resp, &gs))
	require.NotEmpty(t, gs.ID)
	t.Logf("created gameserver %s", gs.ID)

	// Start
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/start", nil)
	require.NoError(t, err)
	resp.Body.Close()

	// Wait for install + ready (combined timeout)
	totalTimeout := installTimeout + readyTimeout
	err = h.WaitForStatus(gs.ID, "running", totalTimeout)
	require.NoError(t, err, "gameserver %s should reach 'running' within %s", gameID, totalTimeout)
	t.Logf("gameserver %s reached running status", gameID)

	// Verify installed
	resp, err = h.Get("/api/gameservers/" + gs.ID)
	require.NoError(t, err)
	var fetched struct {
		Installed bool   `json:"installed"`
		Status    string `json:"status"`
	}
	require.NoError(t, DecodeData(resp, &fetched))
	assert.True(t, fetched.Installed, "installed flag should be set")
	assert.Equal(t, "running", fetched.Status)

	// Stop
	resp, err = h.PostJSON("/api/gameservers/"+gs.ID+"/stop", nil)
	require.NoError(t, err)
	resp.Body.Close()

	require.NoError(t, h.WaitForStatus(gs.ID, "stopped", 30*time.Second))
	t.Logf("gameserver %s stopped cleanly", gameID)

	// Cleanup
	h.Delete("/api/gameservers/" + gs.ID)
}

// buildEnvFromDefaults constructs env vars from the game definition.
// Uses Default values and autogenerate for required fields.
// Skips the test if a required var has no default and no autogenerate.
func buildEnvFromDefaults(t *testing.T, game *games.Game) map[string]string {
	t.Helper()
	env := make(map[string]string)

	for _, v := range game.DefaultEnv {
		if v.System || v.Hidden {
			continue
		}

		value := v.Default

		// Handle consent-required vars (e.g., EULA)
		if v.ConsentRequired {
			value = "true"
		}

		if value == "" && v.Required {
			if v.Autogenerate != "" {
				continue // gamejanitor will auto-fill these
			}
			t.Skipf("game %s has required env var %s with no default and no autogenerate — skipping", game.ID, v.Key)
			return nil
		}

		if value != "" {
			env[v.Key] = value
		}
	}

	return env
}

func envDuration(key string, defaultVal time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return defaultVal
}
