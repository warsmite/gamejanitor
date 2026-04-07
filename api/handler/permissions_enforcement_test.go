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

	var result struct{ ID string }
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.NotEmpty(t, result.ID)
	return result.ID
}

// decodeErrorBody reads the error envelope and returns the error message.
func decodeErrorBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	var errResp struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&errResp))
	return errResp.Error
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
		Role string `json:"role"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "admin", result.Role)
}

func TestPermissions_Me_UserRole(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)
	enableAuth(api)

	scopedToken := testutil.MustCreateUserToken(t, api.Services, nil, nil)

	req := authRequest("GET", api.Server.URL+"/api/me", scopedToken, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Role    string `json:"role"`
		TokenID string `json:"token_id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "user", result.Role)
	assert.NotEmpty(t, result.TokenID)
}

func TestPermissions_Me_AuthDisabled(t *testing.T) {
	t.Parallel()
	api := testutil.NewTestAPI(t)

	resp, err := http.Get(api.Server.URL + "/api/me")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var result struct {
		Role string `json:"role"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	assert.Equal(t, "admin", result.Role)
}

// ---------------------------------------------------------------------------
// Test 2–4: PATCH enforcement, action enforcement, read access scoping
// ---------------------------------------------------------------------------

func TestPermissions_Enforcement(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		permission string
		method     string
		pathSuffix string // appended to /api/gameservers/{id}
		body       any    // if non-nil, marshalled as JSON request body
		wantStatus int
		wantError  string // if non-empty, check error body contains this
	}{
		// PATCH per-field enforcement
		{
			name:       "ConfigureName_Allowed",
			permission: auth.PermGameserverConfigureName,
			method:     "PATCH", pathSuffix: "",
			body: map[string]any{"name": "Renamed"}, wantStatus: http.StatusOK,
		},
		{
			name:       "ConfigureName_BlocksResources",
			permission: auth.PermGameserverConfigureName,
			method:     "PATCH", pathSuffix: "",
			body: map[string]any{"memory_limit_mb": 4096}, wantStatus: http.StatusBadRequest,
			wantError: "missing permission",
		},
		{
			name:       "ConfigureResources_Allowed",
			permission: auth.PermGameserverConfigureResources,
			method:     "PATCH", pathSuffix: "",
			body: map[string]any{"memory_limit_mb": 4096}, wantStatus: http.StatusOK,
		},
		{
			name:       "ConfigureEnv_Allowed",
			permission: auth.PermGameserverConfigureEnv,
			method:     "PATCH", pathSuffix: "",
			body:       map[string]any{"env": map[string]string{"REQUIRED_VAR": "new-value", "KEY": "val"}},
			wantStatus: http.StatusOK,
		},
		{
			name:       "ConfigureEnv_BlocksPorts",
			permission: auth.PermGameserverConfigureEnv,
			method:     "PATCH", pathSuffix: "",
			body:       map[string]any{"ports": []map[string]any{{"host": 27015, "instance": 27015, "protocol": "udp"}}},
			wantStatus: http.StatusBadRequest, wantError: "missing permission",
		},
		{
			name:       "NoConfigurePermission_BlocksAll",
			permission: auth.PermGameserverStart,
			method:     "PATCH", pathSuffix: "",
			body: map[string]any{"name": "new"}, wantStatus: http.StatusBadRequest,
			wantError: "missing permission",
		},

		// Action permission enforcement
		{
			name: "Start_Allowed", permission: auth.PermGameserverStart,
			method: "POST", pathSuffix: "/start", wantStatus: http.StatusOK,
		},
		{
			name: "Start_Denied", permission: auth.PermGameserverStop,
			method: "POST", pathSuffix: "/start", wantStatus: http.StatusForbidden,
		},
		{
			name: "UpdateGame_Allowed", permission: auth.PermGameserverUpdateGame,
			method: "POST", pathSuffix: "/update-game", wantStatus: http.StatusOK,
		},
		{
			name: "UpdateGame_Denied", permission: auth.PermGameserverStart,
			method: "POST", pathSuffix: "/update-game", wantStatus: http.StatusForbidden,
		},
		{
			name: "Reinstall_Denied", permission: auth.PermGameserverStart,
			method: "POST", pathSuffix: "/reinstall", wantStatus: http.StatusForbidden,
		},
		{
			name: "Delete_Denied", permission: auth.PermGameserverStart,
			method: "DELETE", pathSuffix: "", wantStatus: http.StatusForbidden,
		},
		{
			name: "RegenerateSFTP_Denied", permission: auth.PermGameserverStart,
			method: "POST", pathSuffix: "/regenerate-sftp-password", wantStatus: http.StatusForbidden,
		},

		// Read access scoping
		{
			name: "Console_Denied", permission: auth.PermGameserverStart,
			method: "GET", pathSuffix: "/logs", wantStatus: http.StatusForbidden,
		},
		{
			name: "Files_Denied", permission: auth.PermGameserverStart,
			method: "GET", pathSuffix: "/files/", wantStatus: http.StatusForbidden,
		},
		{
			name: "Backups_Denied", permission: auth.PermGameserverStart,
			method: "GET", pathSuffix: "/backups/", wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			api := testutil.NewTestAPI(t)
			enableAuth(api)
			testutil.RegisterFakeWorker(t, api.Services, "worker-1")

			adminToken := testutil.MustCreateAdminToken(t, api.Services)
			gsID := createGameserverWithToken(t, api, adminToken, tc.name)

			token := testutil.MustCreateUserToken(t, api.Services,
				[]string{tc.permission}, []string{gsID})

			var bodyBytes []byte
			if tc.body != nil {
				bodyBytes, _ = json.Marshal(tc.body)
			}

			url := api.Server.URL + "/api/gameservers/" + gsID + tc.pathSuffix
			req := authRequest(tc.method, url, token, bodyBytes)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)

			if tc.wantError != "" {
				errMsg := decodeErrorBody(t, resp)
				assert.Contains(t, errMsg, tc.wantError)
			}
		})
	}
}
