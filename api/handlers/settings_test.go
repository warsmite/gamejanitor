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

func TestAPI_Settings_Get(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/settings")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Equal(t, "ok", result.Status)

	// Should contain known settings
	var settings map[string]any
	json.Unmarshal(result.Data, &settings)
	assert.Contains(t, settings, "auth_enabled")
	assert.Contains(t, settings, "port_range_start")
}

func TestAPI_Settings_Update(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	body, _ := json.Marshal(map[string]any{
		"port_range_start": 30000,
	})
	req, _ := http.NewRequest("PATCH", api.Server.URL+"/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify the value changed
	var result apiResponse
	json.NewDecoder(resp.Body).Decode(&result)
	var settings map[string]any
	json.Unmarshal(result.Data, &settings)

	portStart, ok := settings["port_range_start"]
	assert.True(t, ok)
	assert.Equal(t, float64(30000), portStart)
}

func TestAPI_Settings_Update_UnknownKey(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	body, _ := json.Marshal(map[string]any{
		"nonexistent_setting": true,
	})
	req, _ := http.NewRequest("PATCH", api.Server.URL+"/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPI_Settings_Update_InvalidJSON(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	req, _ := http.NewRequest("PATCH", api.Server.URL+"/api/settings", bytes.NewReader([]byte(`{invalid`)))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
}
