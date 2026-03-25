package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func createGameserverViaAPI(t *testing.T, api *testutil.TestAPI) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"name": "API Helper GS", "game_id": testutil.TestGameID,
		"env": map[string]string{"REQUIRED_VAR": "v"},
	})
	resp, err := http.Post(api.Server.URL+"/api/gameservers", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var result struct{ Data struct{ ID string } }
	json.NewDecoder(resp.Body).Decode(&result)
	require.NotEmpty(t, result.Data.ID)
	return result.Data.ID
}

func TestAPI_Backups_Create(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	body, _ := json.Marshal(map[string]string{"name": "my-backup"})
	resp, err := http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/backups", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	// CreateBackup returns 202 Accepted (async)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "ok", result.Status)

	// Wait for async backup goroutine
	time.Sleep(1 * time.Second)
}

func TestAPI_Backups_List(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/" + gsID + "/backups")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "ok", result.Status)
}

func TestAPI_Backups_Delete_NotFound(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID+"/backups/nonexistent", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	// Should be an error (404 or 500 depending on service error type)
	assert.NotEqual(t, http.StatusNoContent, resp.StatusCode)
}
