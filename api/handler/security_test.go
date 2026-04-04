package handler_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

// Localhost bypass is enabled by default. When auth is on but request comes
// from localhost, the middleware lets it through with no token. This means
// RequireAdmin also passes because token==nil falls through.
// This is intentional for homelab but dangerous for hosted deployments.

func TestSecurity_LocalhostBypass_AdminActionsAllowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	api.Services.SettingsSvc.Set("auth_enabled", true)
	// localhost_bypass defaults to true

	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// POST /api/gameservers requires admin — should work via localhost bypass
	body, _ := json.Marshal(map[string]any{
		"name": "Bypass Test", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	resp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// NOTE: this passes because httptest.Server connects via localhost.
	// In production, a non-localhost request without a token would get 401.
	assert.Equal(t, http.StatusCreated, resp.StatusCode,
		"localhost bypass should allow admin actions without a token")
}

func TestSecurity_LocalhostBypassDisabled_RejectsWithoutToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)

	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
		"with bypass disabled, localhost requests without token should be rejected")
}

func TestSecurity_TokenScopedToGameserver_CannotListOthers(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Create 2 gameservers as admin
	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	var gsIDs []string
	for _, name := range []string{"Visible", "Hidden"} {
		body, _ := json.Marshal(map[string]any{
			"name": name, "game_id": testutil.TestGameID,
			"env": map[string]string{"REQUIRED_VAR": "v"},
		})
		req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
		resp, _ := http.DefaultClient.Do(req)
		var result struct{ Data struct{ ID string } }
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		gsIDs = append(gsIDs, result.Data.ID)
	}

	// Token scoped to first gameserver only
	scopedToken := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverFilesRead}, []string{gsIDs[0]})

	// List gameservers — should only see the scoped one
	req := authRequest("GET", api.Server.URL+"/api/gameservers", scopedToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	var result struct {
		Data []struct{ ID string } `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Len(t, result.Data, 1, "scoped token should only see its own gameserver")
	assert.Equal(t, gsIDs[0], result.Data[0].ID)
}

func TestSecurity_ExpiredToken_Rejected(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	// Create and use an expired token
	past := testutil.PastTime(1) // 1 hour ago
	rawToken, _, err := api.Services.AuthSvc.CreateUserToken("expired", nil, &past, nil)
	require.NoError(t, err)

	req := authRequest("GET", api.Server.URL+"/api/gameservers", rawToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSecurity_StartEndpoint_RequiresCorrectPermission(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Create a gameserver as admin
	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	body, _ := json.Marshal(map[string]any{
		"name": "Perm Test", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
	resp, _ := http.DefaultClient.Do(req)
	var createResult struct{ Data struct{ ID string } }
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()
	gsID := createResult.Data.ID

	// Token with files.read but NOT gameserver.start
	wrongPermToken := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverFilesRead}, nil)

	// Try to start — should be forbidden
	req = authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/start", wrongPermToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token with files.read should not be able to start a gameserver")
}

func TestSecurity_DeleteEndpoint_RequiresDeletePermission(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	body, _ := json.Marshal(map[string]any{
		"name": "Delete Perm", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
	resp, _ := http.DefaultClient.Do(req)
	var createResult struct{ Data struct{ ID string } }
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()
	gsID := createResult.Data.ID

	// Token with start but NOT delete
	startOnlyToken := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, nil)

	req = authRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, startOnlyToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without delete permission should not be able to delete")
}
