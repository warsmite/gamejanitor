package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestAPI_Webhooks_Create(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	body, _ := json.Marshal(map[string]any{
		"url":    "https://example.com/hook",
		"secret": "my-secret",
		"events": []string{"*"},
	})
	resp, err := http.Post(api.Server.URL+"/api/webhooks", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "ok", result.Status)
}

func TestAPI_Webhooks_List(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/webhooks")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPI_Webhooks_Delete(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Create first
	body, _ := json.Marshal(map[string]any{
		"url": "https://example.com/hook", "events": []string{"*"},
	})
	createResp, _ := http.Post(api.Server.URL+"/api/webhooks", "application/json", bytes.NewReader(body))
	var createResult struct{ Data struct{ Endpoint struct{ ID string } } }
	json.NewDecoder(createResp.Body).Decode(&createResult)
	createResp.Body.Close()
	whID := createResult.Data.Endpoint.ID
	require.NotEmpty(t, whID)

	// Delete
	req, _ := http.NewRequest("DELETE", api.Server.URL+"/api/webhooks/"+whID, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}

func TestAPI_Webhooks_Create_InvalidJSON(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Post(api.Server.URL+"/api/webhooks", "application/json", bytes.NewReader([]byte(`{bad`)))
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPI_Webhooks_Get(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Create
	body, _ := json.Marshal(map[string]any{
		"url": "https://example.com/hook", "events": []string{"gameserver.*"},
	})
	createResp, _ := http.Post(api.Server.URL+"/api/webhooks", "application/json", bytes.NewReader(body))
	var createResult struct{ Data struct{ Endpoint struct{ ID string } } }
	json.NewDecoder(createResp.Body).Decode(&createResult)
	createResp.Body.Close()
	whID := createResult.Data.Endpoint.ID

	// Get
	resp, err := http.Get(api.Server.URL + "/api/webhooks/" + whID)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Data struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"data"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, whID, result.Data.ID)
	assert.Equal(t, "https://example.com/hook", result.Data.URL)
}

func TestAPI_Webhooks_Deliveries(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Create webhook
	body, _ := json.Marshal(map[string]any{
		"url": "https://example.com/hook", "events": []string{"*"},
	})
	createResp, _ := http.Post(api.Server.URL+"/api/webhooks", "application/json", bytes.NewReader(body))
	var createResult struct{ Data struct{ Endpoint struct{ ID string } } }
	json.NewDecoder(createResp.Body).Decode(&createResult)
	createResp.Body.Close()
	whID := createResult.Data.Endpoint.ID

	// List deliveries (should be empty)
	resp, err := http.Get(api.Server.URL + "/api/webhooks/" + whID + "/deliveries")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
