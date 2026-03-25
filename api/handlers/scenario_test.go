package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

// API scenario tests simulate real client interactions with the HTTP API.
// These catch routing, middleware ordering, and response format issues that
// service-layer tests can't.

func TestAPIScenario_Newbie_FullWorkflowNoAuth(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// 1. List games — should include test-game
	resp, err := http.Get(api.Server.URL + "/api/games")
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 2. Create a gameserver
	body, _ := json.Marshal(map[string]any{
		"name": "My Server", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	resp, err = http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var createResult struct {
		Status string `json:"status"`
		Data   struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			SFTPUsername   string `json:"sftp_username"`
			SFTPPassword   string `json:"sftp_password"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()

	assert.Equal(t, "ok", createResult.Status)
	gsID := createResult.Data.ID
	require.NotEmpty(t, gsID)
	assert.NotEmpty(t, createResult.Data.SFTPUsername)
	assert.NotEmpty(t, createResult.Data.SFTPPassword, "create response should include SFTP password")

	// 3. Get the gameserver
	resp, err = http.Get(api.Server.URL + "/api/gameservers/" + gsID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 4. Start it
	resp, err = http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/start", "", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 5. Stop it
	resp, err = http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/stop", "", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 6. Delete it
	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
	resp.Body.Close()

	// 7. Verify it's gone
	resp, err = http.Get(api.Server.URL + "/api/gameservers/" + gsID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp.Body.Close()
}

func TestAPIScenario_Business_AuthEnforced(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Enable business-like auth
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)

	// 1. Unauthenticated request → 401
	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	resp.Body.Close()

	// 2. Create admin token
	adminToken := testutil.MustCreateAdminToken(t, api.Services)

	// 3. Authenticated list → 200
	req, _ := http.NewRequest("GET", api.Server.URL+"/api/gameservers", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 4. Create a gameserver as admin
	body, _ := json.Marshal(map[string]any{
		"name": "Business Server", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	req, _ = http.NewRequest("POST", api.Server.URL+"/api/gameservers", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var createResult struct{ Data struct{ ID string } }
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()
	gsID := createResult.Data.ID

	// 5. Create a limited operator token
	operatorToken := testutil.MustCreateCustomToken(t, api.Services,
		[]string{service.PermGameserverStart, service.PermGameserverStop}, nil)

	// 6. Operator can start
	req, _ = http.NewRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/start", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 7. Operator cannot delete
	req, _ = http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()

	// 8. Operator cannot manage tokens
	req, _ = http.NewRequest("GET", api.Server.URL+"/api/tokens", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
	resp.Body.Close()
}

func TestAPIScenario_ResponseFormat_ConsistentEnvelope(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Every API response should use {"status": "ok/error", ...}

	// Success
	resp, _ := http.Get(api.Server.URL + "/api/games")
	var okResult map[string]any
	json.NewDecoder(resp.Body).Decode(&okResult)
	resp.Body.Close()
	assert.Equal(t, "ok", okResult["status"])
	assert.Contains(t, okResult, "data")

	// Not found
	resp, _ = http.Get(api.Server.URL + "/api/gameservers/nonexistent")
	var errResult map[string]any
	json.NewDecoder(resp.Body).Decode(&errResult)
	resp.Body.Close()
	assert.Equal(t, "error", errResult["status"])
	assert.Contains(t, errResult, "error")

	// Health endpoint is the exception — returns plain text
	resp, _ = http.Get(api.Server.URL + "/health")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
