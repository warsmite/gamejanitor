package handler_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
		ID            string `json:"id"`
		Name          string `json:"name"`
		SFTPUsername   string `json:"sftp_username"`
		SFTPPassword   string `json:"sftp_password"`
	}
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()

	gsID := createResult.ID
	require.NotEmpty(t, gsID)
	assert.NotEmpty(t, createResult.SFTPUsername)
	assert.NotEmpty(t, createResult.SFTPPassword, "create response should include SFTP password")

	// 3. Get the gameserver
	resp, err = http.Get(api.Server.URL + "/api/gameservers/" + gsID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()

	// 4. Start it
	resp, err = http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/actions/start", "", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// 5. Stop it
	resp, err = http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/actions/stop", "", nil)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// Wait for stop to complete before deleting (operations are async)
	require.Eventually(t, func() bool {
		resp, err = http.Get(api.Server.URL + "/api/gameservers/" + gsID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body struct{ Status string }
		json.NewDecoder(resp.Body).Decode(&body)
		return body.Status == "stopped"
	}, 5*time.Second, 100*time.Millisecond, "gameserver should reach stopped")

	// 6. Delete it
	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, nil)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
	resp.Body.Close()

	// 7. Verify it's gone (async — wait for background cleanup)
	require.Eventually(t, func() bool {
		resp, err = http.Get(api.Server.URL + "/api/gameservers/" + gsID)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusNotFound
	}, 5*time.Second, 50*time.Millisecond, "gameserver should be deleted")
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

	var createResult struct{ ID string }
	json.NewDecoder(resp.Body).Decode(&createResult)
	resp.Body.Close()
	gsID := createResult.ID

	// 5. Create a limited operator token with access to the created gameserver
	operatorToken := testutil.MustCreateUserToken(t, api.Services,
		[]string{auth.PermGameserverStart, auth.PermGameserverStop}, []string{gsID})

	// 6. Operator can start
	req, _ = http.NewRequest("POST", api.Server.URL+"/api/gameservers/"+gsID+"/actions/start", nil)
	req.Header.Set("Authorization", "Bearer "+operatorToken)
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)
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

func TestAPIScenario_ResponseFormat_NoEnvelope(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Success — data returned directly (no wrapper)
	resp, _ := http.Get(api.Server.URL + "/api/games")
	var games []map[string]any
	json.NewDecoder(resp.Body).Decode(&games)
	resp.Body.Close()
	assert.IsType(t, []map[string]any{}, games, "success response should be the data directly")

	// Error — {"error": "message"}
	resp, _ = http.Get(api.Server.URL + "/api/gameservers/nonexistent")
	var errResult map[string]any
	json.NewDecoder(resp.Body).Decode(&errResult)
	resp.Body.Close()
	assert.Contains(t, errResult, "error")
	assert.NotContains(t, errResult, "status", "error response should not contain status field")

	// Health endpoint — plain text
	resp, _ = http.Get(api.Server.URL + "/health")
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	resp.Body.Close()
}
