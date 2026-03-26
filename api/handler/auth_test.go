package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

type apiResponse struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func TestAPI_HealthEndpoint_NoAuthRequired(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Enable auth
	api.Services.SettingsSvc.Set("auth_enabled", true)

	resp, err := http.Get(api.Server.URL + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPI_AuthDisabled_AllowsRequests(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	// Auth is disabled by default
	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPI_AuthEnabled_RejectsMissingToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)

	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAPI_AuthEnabled_AcceptsValidToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)

	token := testutil.MustCreateAdminToken(t, api.Services)

	req, _ := http.NewRequest("GET", api.Server.URL+"/api/gameservers", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAPI_AuthEnabled_RejectsInvalidToken(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	api.Services.SettingsSvc.Set("auth_enabled", true)
	api.Services.SettingsSvc.Set("localhost_bypass", false)

	req, _ := http.NewRequest("GET", api.Server.URL+"/api/gameservers", nil)
	req.Header.Set("Authorization", "Bearer gj_invalid_token_here")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAPI_SecurityHeaders(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/gameservers")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "DENY", resp.Header.Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
}
