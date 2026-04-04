package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/testutil"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createGameserverWithToken creates a gameserver using the given token and returns its ID.
func createGameserverWithToken(t *testing.T, api *testutil.TestAPI, adminToken, name string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": name, "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req := authRequest("POST", api.Server.URL+"/api/gameservers", adminToken, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode, "admin should be able to create a gameserver")

	var result struct{ Data struct{ ID string } }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.Data.ID)
	return result.Data.ID
}

// decodeErrorBody reads the error envelope and returns the error message.
func decodeErrorBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var envelope struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&envelope))
	return envelope.Error
}

// ---------------------------------------------------------------------------
// Test 1: Permissions in list response
// ---------------------------------------------------------------------------

func TestPermissions_Me_AdminRole(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	adminToken := testutil.MustCreateAdminToken(t, api.Services)

	req := authRequest("GET", api.Server.URL+"/api/me", adminToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Role string `json:"role"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "admin", result.Data.Role)
}

func TestPermissions_Me_UserRole(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	scopedToken := testutil.MustCreateCustomToken(t, api.Services, nil, nil)

	req := authRequest("GET", api.Server.URL+"/api/me", scopedToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Role    string `json:"role"`
			TokenID string `json:"token_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "user", result.Data.Role)
	assert.NotEmpty(t, result.Data.TokenID)
}

func TestPermissions_Me_AuthDisabled(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/me")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			Role string `json:"role"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "admin", result.Data.Role)
}

// ---------------------------------------------------------------------------
// Test 2: PATCH per-field enforcement
// ---------------------------------------------------------------------------

func TestPermissions_Patch_ConfigureName_Allowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch Name")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverConfigureName}, []string{gsID})

	body, _ := json.Marshal(map[string]any{"name": "Renamed"})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"token with configure.name should be able to rename")
}

func TestPermissions_Patch_ConfigureName_BlocksResources(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch Block Resources")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverConfigureName}, []string{gsID})

	body, _ := json.Marshal(map[string]any{"memory_limit_mb": 4096})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	errMsg := decodeErrorBody(t, resp)
	assert.Contains(t, errMsg, "missing permission",
		"should report missing permission for resource field")
}

func TestPermissions_Patch_ConfigureResources_Allowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch Resources")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverConfigureResources}, []string{gsID})

	body, _ := json.Marshal(map[string]any{"memory_limit_mb": 4096})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"token with configure.resources should be able to set memory_limit_mb")
}

func TestPermissions_Patch_ConfigureEnv_Allowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch Env")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverConfigureEnv}, []string{gsID})

	body, _ := json.Marshal(map[string]any{
		"env": map[string]string{"REQUIRED_VAR": "new-value", "KEY": "val"},
	})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"token with configure.env should be able to set env vars")
}

func TestPermissions_Patch_ConfigureEnv_BlocksPorts(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch Env Blocks Ports")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverConfigureEnv}, []string{gsID})

	body, _ := json.Marshal(map[string]any{
		"ports": []map[string]any{
			{"host": 27015, "instance": 27015, "protocol": "udp"},
		},
	})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	errMsg := decodeErrorBody(t, resp)
	assert.Contains(t, errMsg, "missing permission",
		"configure.env token should not be able to modify ports")
}

func TestPermissions_Patch_NoConfigurePermission_BlocksAll(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Patch No Configure")

	// Token with only start — no configure.* permissions
	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	body, _ := json.Marshal(map[string]any{"name": "new"})
	req := authRequest("PATCH", api.Server.URL+"/api/gameservers/"+gsID, token, body)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode,
		"token without any configure permission should be denied a PATCH")
	errMsg := decodeErrorBody(t, resp)
	assert.Contains(t, errMsg, "missing permission")
}

// ---------------------------------------------------------------------------
// Test 3: Action permission enforcement
// ---------------------------------------------------------------------------

func TestPermissions_Start_Allowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Start Allowed")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/start", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"token with gameserver.start should be allowed to start")
}

func TestPermissions_Start_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Start Denied")

	// Token with stop but NOT start
	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStop}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/start", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.start should be forbidden")
}

func TestPermissions_UpdateGame_Allowed(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "UpdateGame Allowed")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverUpdateGame}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/update-game", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"token with gameserver.update-game should be allowed")
}

func TestPermissions_UpdateGame_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "UpdateGame Denied")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/update-game", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.update-game should be forbidden")
}

func TestPermissions_Reinstall_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Reinstall Denied")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/reinstall", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.reinstall should be forbidden")
}

func TestPermissions_Delete_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Delete Denied")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.delete should be forbidden")
}

func TestPermissions_RegenerateSFTP_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "RegenSFTP Denied")

	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/regenerate-sftp-password", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.regenerate-sftp should be forbidden")
}

// ---------------------------------------------------------------------------
// Test 4: Read access scoping
// ---------------------------------------------------------------------------

func TestPermissions_Console_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Console Denied")

	// Token without gameserver.logs
	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("GET", api.Server.URL+"/api/gameservers/"+gsID+"/logs", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.logs should be forbidden from /logs")
}

func TestPermissions_Files_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Files Denied")

	// Token without gameserver.files.read
	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("GET", api.Server.URL+"/api/gameservers/"+gsID+"/files/", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without gameserver.files.read should be forbidden from /files")
}

func TestPermissions_Backups_Denied(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	adminToken := testutil.MustCreateAdminToken(t, api.Services)
	gsID := createGameserverWithToken(t, api, adminToken, "Backups Denied")

	// Token without backup.read
	token := testutil.MustCreateCustomToken(t, api.Services,
		[]string{auth.PermGameserverStart}, []string{gsID})

	req := authRequest("GET", api.Server.URL+"/api/gameservers/"+gsID+"/backups/", token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusForbidden, resp.StatusCode,
		"token without backup.read should be forbidden from /backups")
}
