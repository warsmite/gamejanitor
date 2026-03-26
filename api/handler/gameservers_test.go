package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestAPI_ListGameservers_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
}

func TestAPI_CreateGameserver_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	body := map[string]any{
		"name":    "API Test Server",
		"game_id": testutil.TestGameID,
		"env":     map[string]string{"REQUIRED_VAR": "hello"},
	}
	bodyJSON, _ := json.Marshal(body)

	resp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(bodyJSON))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
}

func TestAPI_CreateGameserver_InvalidJSON(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader([]byte(`{invalid`)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "error", result.Status)
}

func TestAPI_GetGameserver_NotFound(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/nonexistent-id")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAPI_GetGameserver_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Create via API
	body := map[string]any{
		"name":    "Get Test",
		"game_id": testutil.TestGameID,
		"env":     map[string]string{"REQUIRED_VAR": "hello"},
	}
	bodyJSON, _ := json.Marshal(body)
	createResp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(bodyJSON))
	require.NoError(t, err)
	defer createResp.Body.Close()
	require.Equal(t, http.StatusCreated, createResp.StatusCode)

	var createResult struct {
		Status string `json:"status"`
		Data   struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(createResp.Body).Decode(&createResult))
	gsID := createResult.Data.ID
	require.NotEmpty(t, gsID)

	// Get it and verify response body has the right data
	getResp, err := http.Get(api.Server.URL + "/api/gameservers/" + gsID)
	require.NoError(t, err)
	defer getResp.Body.Close()

	assert.Equal(t, http.StatusOK, getResp.StatusCode)

	var getResult struct {
		Status string `json:"status"`
		Data   struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			GameID string `json:"game_id"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&getResult))
	assert.Equal(t, gsID, getResult.Data.ID)
	assert.Equal(t, "Get Test", getResult.Data.Name)
	assert.Equal(t, testutil.TestGameID, getResult.Data.GameID)
}

func TestAPI_DeleteGameserver_Success(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")

	// Create first
	body := map[string]any{
		"name":    "Delete Test",
		"game_id": testutil.TestGameID,
		"env":     map[string]string{"REQUIRED_VAR": "hello"},
	}
	bodyJSON, _ := json.Marshal(body)
	createResp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(bodyJSON))
	require.NoError(t, err)
	defer createResp.Body.Close()

	var createResult struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.NewDecoder(createResp.Body).Decode(&createResult)
	gsID := createResult.Data.ID

	// Delete
	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID, nil)
	delResp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer delResp.Body.Close()

	assert.Equal(t, http.StatusNoContent, delResp.StatusCode)

	// Verify it's actually gone
	getResp, err := http.Get(api.Server.URL + "/api/gameservers/" + gsID)
	require.NoError(t, err)
	defer getResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, getResp.StatusCode)
}

func TestAPI_ResponseEnvelope(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Success response has "status": "ok"
	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	var result apiResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "ok", result.Status)
	assert.Empty(t, result.Error)

	// Error response has "status": "error"
	errResp, err := http.Get(api.Server.URL + "/api/gameservers/nonexistent")
	require.NoError(t, err)
	defer errResp.Body.Close()

	var errResult apiResponse
	require.NoError(t, json.NewDecoder(errResp.Body).Decode(&errResult))
	assert.Equal(t, "error", errResult.Status)
	assert.NotEmpty(t, errResult.Error)
}
