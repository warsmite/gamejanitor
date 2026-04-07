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

func TestAPI_Schedules_Create(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	body, _ := json.Marshal(map[string]any{
		"name": "daily-restart", "type": "restart",
		"cron_expr": "0 4 * * *",
	})
	resp, err := http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/schedules", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	assert.NotEmpty(t, result["id"])
}

func TestAPI_Schedules_Create_InvalidCron(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	body, _ := json.Marshal(map[string]any{
		"name": "bad", "type": "restart",
		"cron_expr": "not valid",
	})
	resp, err := http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/schedules", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPI_Schedules_Create_MissingFields(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	// Missing type and cron_expr
	body, _ := json.Marshal(map[string]any{"name": "incomplete"})
	resp, err := http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/schedules", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPI_Schedules_List(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	resp, err := http.Get(api.Server.URL + "/api/gameservers/" + gsID + "/schedules")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPI_Schedules_Delete(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	testutil.RegisterFakeWorker(t, api.Services, "worker-1")
	gsID := createGameserverViaAPI(t, api)

	// Create one first
	body, _ := json.Marshal(map[string]any{
		"name": "to-delete", "type": "restart", "cron_expr": "0 0 * * *",
	})
	createResp, _ := http.Post(api.Server.URL+"/api/gameservers/"+gsID+"/schedules", "application/json", bytes.NewReader(body))
	var createResult struct{ ID string }
	json.NewDecoder(createResp.Body).Decode(&createResult)
	createResp.Body.Close()
	schedID := createResult.ID
	require.NotEmpty(t, schedID)

	// Delete
	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/gameservers/"+gsID+"/schedules/"+schedID, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
