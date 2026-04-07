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

func TestAPI_Settings_Get(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/settings")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Settings map[string]any `json:"settings"`
		Config   map[string]any `json:"config"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	assert.Contains(t, result.Settings, "auth_enabled")
	assert.Contains(t, result.Settings, "port_range_start")
	assert.Contains(t, result.Config, "bind")
	assert.Contains(t, result.Config, "sftp_port")
}

func TestAPI_Settings_Update(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Use a value within the default range (27000-28999)
	body, _ := json.Marshal(map[string]any{
		"port_range_start": 27500,
	})
	req, _ := http.NewRequest("PATCH", api.Server.URL+"/api/settings", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Verify the value changed
	var result struct {
		Settings map[string]any `json:"settings"`
	}
	json.NewDecoder(resp.Body).Decode(&result)

	portStart, ok := result.Settings["port_range_start"]
	assert.True(t, ok)
	assert.Equal(t, float64(27500), portStart)
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
