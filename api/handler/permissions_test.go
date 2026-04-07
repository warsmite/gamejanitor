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

func enableAuth(api *testutil.TestAPI) {
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)
}

func authRequest(method, url, token string, body []byte) *http.Request {
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, reader)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestAPI_AdminRequired_CreateGameserver_RejectsCustomToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Custom token with gameserver.start but NOT admin
	customToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverStart}, nil)

	body, _ := json.Marshal(map[string]any{
		"name": "Denied", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})

	req := authRequest("POST", api.Server.URL+"/api/gameservers", customToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAPI_AdminRequired_CreateGameserver_AcceptsAdminToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)

	body, _ := json.Marshal(map[string]any{
		"name": "Allowed", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})

	req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestAPI_GameserverScoping_CustomTokenCannotAccessOtherGameserver(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Create two gameservers as admin
	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	var gsIDs []string
	for _, name := range []string{"Server A", "Server B"} {
		body, _ := json.Marshal(map[string]any{
			"name": name, "game_id": testutil.TestGameID,
			"env": map[string]string{"REQUIRED_VAR": "v"},
		})
		req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		var result struct{ ID string }
		json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		gsIDs = append(gsIDs, result.ID)
	}

	// Custom token scoped to gs A only, with access permission
	scopedToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverFilesRead}, []string{gsIDs[0]})

	// Access gs A — should work
	req := authRequest("GET", api.Server.URL+"/api/gameservers/"+gsIDs[0], scopedToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Access gs B — should be forbidden
	req = authRequest("GET", api.Server.URL+"/api/gameservers/"+gsIDs[1], scopedToken, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAPI_TokensEndpoint_RequiresTokensManage(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	// Custom token without tokens.manage
	customToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverStart}, nil)

	req := authRequest("GET", api.Server.URL+"/api/tokens", customToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAPI_SettingsEndpoint_RequiresSettingsView(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	customToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverStart}, nil)

	req := authRequest("GET", api.Server.URL+"/api/settings", customToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAPI_ClusterEndpoint_RequiresAdmin(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	customToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverStart}, nil)

	req := authRequest("GET", api.Server.URL+"/api/cluster", customToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

// mustCreateCanCreateToken creates a user token with can_create=true via the API.
func mustCreateCanCreateToken(t *testing.T, api *testutil.TestAPI) string {
	t.Helper()
	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	body, _ := json.Marshal(map[string]any{
		"name": "creator", "role": "user", "can_create": true,
	})
	req := authRequest("POST", api.Server.URL+"/api/tokens", adminToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var result struct{ Token string }
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Token
}

func TestAPI_CanCreate_AllowsGameserverCreation(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	creatorToken := mustCreateCanCreateToken(t, api)

	body, _ := json.Marshal(map[string]any{
		"name": "My Server", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req := authRequest("POST", api.Server.URL+"/api/gameservers", creatorToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)
}

func TestAPI_CanCreateFalse_BlocksGameserverCreation(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// User token without can_create
	noCreateToken := testutil.MustCreateUserToken(t, api.Services, nil, nil)

	body, _ := json.Marshal(map[string]any{
		"name": "Blocked", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req := authRequest("POST", api.Server.URL+"/api/gameservers", noCreateToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}

func TestAPI_CanCreate_CannotAccessClusterRoutes(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	creatorToken := mustCreateCanCreateToken(t, api)

	// Tokens
	req := authRequest("GET", api.Server.URL+"/api/tokens", creatorToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "can_create should not grant tokens access")

	// Settings
	req = authRequest("GET", api.Server.URL+"/api/settings", creatorToken, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "can_create should not grant settings access")

	// Cluster
	req = authRequest("GET", api.Server.URL+"/api/cluster", creatorToken, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "can_create should not grant cluster access")

	// Webhooks
	req = authRequest("GET", api.Server.URL+"/api/webhooks", creatorToken, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusForbidden, resp.StatusCode, "can_create should not grant webhooks access")
}
