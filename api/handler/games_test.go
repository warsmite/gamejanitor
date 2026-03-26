package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestAPI_ListGames_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/games")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)

	var games []map[string]any
	require.NoError(t, json.Unmarshal(result.Data, &games))
	assert.GreaterOrEqual(t, len(games), 10, "should include embedded games")

	// Verify test-game is in the list
	found := false
	for _, g := range games {
		if g["id"] == testutil.TestGameID {
			found = true
			break
		}
	}
	assert.True(t, found, "test-game should be in the games list")
}

func TestAPI_GetGame_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/games/" + testutil.TestGameID)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)

	var game map[string]any
	require.NoError(t, json.Unmarshal(result.Data, &game))
	assert.Equal(t, testutil.TestGameID, game["id"])
	assert.Equal(t, "Test Game", game["name"])
}

func TestAPI_GetGame_NotFound(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/games/nonexistent-game")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}
