//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_Permissions_FullFlow tests the complete permission system end-to-end:
// 1. Enable auth, get admin token
// 2. Admin creates a gameserver
// 3. Admin creates a user token (can_create=false)
// 4. User token cannot see admin's gameserver (no grant)
// 5. Admin grants user access to the gameserver
// 6. User token can now see and interact with the gameserver
// 7. Admin creates a user token with can_create=true
// 8. Creator token creates its own gameserver and can see it (ownership)
// 9. Creator token cannot see admin's gameserver (no grant)
// This test mutates global auth settings (enables auth, disables localhost
// bypass), so it must NOT run in parallel with other tests. We avoid Start(t)
// which calls t.Parallel(), and set up the harness manually instead.
func TestE2E_Permissions_FullFlow(t *testing.T) {
	h := startSerial(t)

	// 1. Create admin token BEFORE enabling auth (localhost bypass lets us through)
	tokenName := fmt.Sprintf("e2e-admin-%d", time.Now().UnixNano())
	resp, err := h.PostJSON("/api/tokens", map[string]any{
		"name": tokenName, "role": "admin",
	})
	require.NoError(t, err)
	var adminResult struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&adminResult))
	resp.Body.Close()
	adminToken := adminResult.Token
	require.NotEmpty(t, adminToken)

	// Enable auth first, then disable localhost bypass (must be in two steps —
	// the handler rejects disabling bypass before auth is enabled).
	resp, err = h.AuthPatch("/api/settings", adminToken, map[string]any{
		"auth_enabled": true,
	})
	require.NoError(t, err)
	resp.Body.Close()

	resp, err = h.AuthPatch("/api/settings", adminToken, map[string]any{
		"localhost_bypass": false,
	})
	require.NoError(t, err)
	resp.Body.Close()

	// Cleanup: restore settings so other tests aren't affected
	t.Cleanup(func() {
		h.AuthPatch("/api/settings", adminToken, map[string]any{
			"auth_enabled":     false,
			"localhost_bypass": true,
		})
	})

	// 2. Admin creates a gameserver
	resp, err = h.AuthPost("/api/gameservers", adminToken, map[string]any{
		"name":    "E2E Admin Server",
		"game_id": h.GameID(),
		"env":     testGameEnv(h, nil),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "admin should create gameserver")
	var gsResult struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&gsResult)
	resp.Body.Close()
	adminGsID := gsResult.ID
	require.NotEmpty(t, adminGsID)

	t.Cleanup(func() {
		h.AuthPost("/api/gameservers/"+adminGsID+"/actions/stop", adminToken, nil)
		req, _ := http.NewRequest("DELETE", h.BaseURL+"/api/gameservers/"+adminGsID, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		http.DefaultClient.Do(req)
	})

	// 3. Admin creates a user token (view only, no can_create)
	resp, err = h.AuthPost("/api/tokens", adminToken, map[string]any{
		"name": "e2e-viewer", "role": "user",
	})
	require.NoError(t, err)
	var viewerResult struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	json.NewDecoder(resp.Body).Decode(&viewerResult)
	resp.Body.Close()
	viewerToken := viewerResult.Token
	viewerTokenID := viewerResult.TokenID
	require.NotEmpty(t, viewerToken)

	// 4. Viewer cannot see admin's gameserver (no grant)
	resp, err = h.AuthGet("/api/gameservers", viewerToken)
	require.NoError(t, err)
	var listResult []struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&listResult)
	resp.Body.Close()
	assert.Len(t, listResult, 0, "viewer with no grants should see no gameservers")

	// 5. Admin grants viewer access to the gameserver with start permission
	resp, err = h.AuthPatch("/api/gameservers/"+adminGsID, adminToken, map[string]any{
		"grants": map[string][]string{
			viewerTokenID: {"gameserver.start", "gameserver.stop"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "admin should be able to patch grants")
	resp.Body.Close()

	// 6. Viewer can now see the gameserver
	resp, err = h.AuthGet("/api/gameservers", viewerToken)
	require.NoError(t, err)
	json.NewDecoder(resp.Body).Decode(&listResult)
	resp.Body.Close()
	assert.Len(t, listResult, 1, "viewer with grant should see the gameserver")
	if len(listResult) > 0 {
		assert.Equal(t, adminGsID, listResult[0].ID)
	}

	// 7. Viewer cannot create a gameserver (no can_create)
	resp, err = h.AuthPost("/api/gameservers", viewerToken, map[string]any{
		"name":    "Should Fail",
		"game_id": h.GameID(),
		"env":     testGameEnv(h, nil),
	})
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "viewer without can_create should be blocked")
	resp.Body.Close()

	// 8. Viewer cannot access cluster routes
	resp, err = h.AuthGet("/api/settings", viewerToken)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "viewer should not see settings")
	resp.Body.Close()

	resp, err = h.AuthGet("/api/tokens", viewerToken)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "viewer should not see tokens")
	resp.Body.Close()

	// 9. Admin creates a creator token
	resp, err = h.AuthPost("/api/tokens", adminToken, map[string]any{
		"name": "e2e-creator", "role": "user", "can_create": true,
		"max_gameservers": 2,
	})
	require.NoError(t, err)
	var creatorResult struct {
		Token   string `json:"token"`
		TokenID string `json:"token_id"`
	}
	json.NewDecoder(resp.Body).Decode(&creatorResult)
	resp.Body.Close()
	creatorToken := creatorResult.Token
	require.NotEmpty(t, creatorToken)

	// 10. Creator creates a gameserver (ownership)
	resp, err = h.AuthPost("/api/gameservers", creatorToken, map[string]any{
		"name":    "E2E Creator Server",
		"game_id": h.GameID(),
		"env":     testGameEnv(h, nil),
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "creator should create gameserver")
	var creatorGsResult struct {
		ID string `json:"id"`
	}
	json.NewDecoder(resp.Body).Decode(&creatorGsResult)
	resp.Body.Close()
	creatorGsID := creatorGsResult.ID

	t.Cleanup(func() {
		h.AuthPost("/api/gameservers/"+creatorGsID+"/actions/stop", adminToken, nil)
		req, _ := http.NewRequest("DELETE", h.BaseURL+"/api/gameservers/"+creatorGsID, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		http.DefaultClient.Do(req)
	})

	// 11. Creator sees only their own server (not admin's)
	resp, err = h.AuthGet("/api/gameservers", creatorToken)
	require.NoError(t, err)
	json.NewDecoder(resp.Body).Decode(&listResult)
	resp.Body.Close()
	assert.Len(t, listResult, 1, "creator should see only owned gameserver")
	if len(listResult) > 0 {
		assert.Equal(t, creatorGsID, listResult[0].ID)
	}

	// 12. /api/me returns correct info for each token
	resp, err = h.AuthGet("/api/me", adminToken)
	require.NoError(t, err)
	var meResult struct {
		Role string `json:"role"`
	}
	json.NewDecoder(resp.Body).Decode(&meResult)
	resp.Body.Close()
	assert.Equal(t, "admin", meResult.Role)

	resp, err = h.AuthGet("/api/me", viewerToken)
	require.NoError(t, err)
	json.NewDecoder(resp.Body).Decode(&meResult)
	resp.Body.Close()
	assert.Equal(t, "user", meResult.Role)
}
