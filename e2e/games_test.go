//go:build e2e

package e2e

// games_test.go — game-definition compatibility. Iterates over the games
// specified by E2E_GAMES and runs a full lifecycle against each. Used to
// verify new/edited game YAML works end-to-end.
//
// Not to be confused with "does the system come up" — the regular
// TestGameserver_Basic already covers that against E2E_GAME_ID.

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/warsmite/gamejanitor/sdk"

	"github.com/warsmite/gamejanitor/games"
)

// TestGame_Compatibility runs a lifecycle against each game in E2E_GAMES.
// E2E_GAMES=all walks every embedded definition; comma-separated IDs select
// specific games. Defaults to "minecraft-java" (one real game as a smoke).
func TestGame_Compatibility(t *testing.T) {
	for _, gameID := range selectCompatibilityGames(t) {
		gameID := gameID
		t.Run(gameID, func(t *testing.T) {
			runCompatibility(t, gameID)
		})
	}
}

func selectCompatibilityGames(t *testing.T) []string {
	t.Helper()
	if v := os.Getenv("E2E_GAMES"); v != "" {
		if v == "all" {
			store, err := games.NewGameStore("", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
			require.NoError(t, err, "load game store")
			var ids []string
			for _, g := range store.ListGames() {
				ids = append(ids, g.ID)
			}
			return ids
		}
		return strings.Split(v, ",")
	}
	return []string{"minecraft-java"}
}

func runCompatibility(t *testing.T, gameID string) {
	env := NewEnv(t)

	// Load the game def to build env from defaults + consent vars.
	store, err := games.NewGameStore("", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	require.NoError(t, err, "load game store")
	game := store.GetGame(gameID)
	if game == nil {
		t.Skipf("game %q not found", gameID)
	}

	envVars := buildCompatibilityEnv(t, game)
	if envVars == nil {
		return // skipped due to unfillable required vars
	}

	// Direct SDK call because we need a custom game_id, not env.GameID().
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	created, err := env.sdk.Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name:   "game-compat-" + gameID,
		GameID: gameID,
		Env:    envVars,
	})
	require.NoError(t, err, "create %s", gameID)

	gs := &Gameserver{env: env, id: created.ID}
	t.Cleanup(gs.teardown)

	gs.Start().MustBeRunning()
	assert.True(t, gs.Snapshot().Installed, "installed flag should be set")
	gs.Stop().MustBeStopped()
}

// buildCompatibilityEnv fills env from game defaults, handling consent
// (EULA etc.) automatically. Returns nil to skip if required vars can't
// be filled.
func buildCompatibilityEnv(t *testing.T, game *games.Game) map[string]string {
	t.Helper()
	envVars := make(map[string]string)
	for _, v := range game.DefaultEnv {
		if v.System || v.Hidden {
			continue
		}
		value := v.Default
		if v.ConsentRequired {
			value = "true"
		}
		if value == "" && v.Required {
			if v.Autogenerate != "" {
				continue // controller will auto-fill
			}
			t.Skipf("game %s: required var %q has no default/autogenerate", game.ID, v.Key)
			return nil
		}
		if value != "" {
			envVars[v.Key] = value
		}
	}
	return envVars
}
