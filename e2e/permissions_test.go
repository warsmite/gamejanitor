//go:build e2e

package e2e

// permissions_test.go — auth, tokens, grants, quotas, scoping. These tests
// mutate global settings (auth_enabled, localhost_bypass), so they run
// serially and clean up after themselves.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdk "github.com/warsmite/gamejanitor/sdk"
)

// enableAuth creates an admin token, enables auth, and returns the admin
// token. Cleanup is registered to restore settings at test end.
func enableAuth(t *testing.T, env *Env) *Token {
	t.Helper()

	admin := env.NewToken(sdk.CreateTokenRequest{
		Name: "e2e-admin-" + t.Name(),
		Role: "admin",
	})

	env.SetSetting("auth_enabled", true)
	env.SetSettingAs(admin, "localhost_bypass", false)

	t.Cleanup(func() {
		env.SetSettingAs(admin, "localhost_bypass", true)
		env.SetSettingAs(admin, "auth_enabled", false)
	})

	return admin
}

// TestPermissions_Scoping verifies that tokens with no grants see no
// gameservers other than their own owned ones.
func TestPermissions_Scoping(t *testing.T) {
	env := NewEnvSerial(t)
	admin := enableAuth(t, env)

	// Admin creates a gameserver.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminGs, err := admin.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name:   "admin-" + t.Name(),
		GameID: env.GameID(),
		Env:    env.GameEnv(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.SDK().Gameservers.Delete(context.Background(), adminGs.ID) })

	viewer := env.NewToken(sdk.CreateTokenRequest{Name: "viewer-" + t.Name(), Role: "user"})

	// Viewer without grants sees zero gameservers.
	resp, err := viewer.SDK().Gameservers.List(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, resp.Gameservers, "viewer with no grants should see nothing")
}

// TestPermissions_Grants verifies that granting access lets a scoped token
// see and act on a specific gameserver.
func TestPermissions_Grants(t *testing.T) {
	env := NewEnvSerial(t)
	admin := enableAuth(t, env)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adminGs, err := admin.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name:   "granted-" + t.Name(),
		GameID: env.GameID(),
		Env:    env.GameEnv(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = admin.SDK().Gameservers.Delete(context.Background(), adminGs.ID) })

	viewer := env.NewToken(sdk.CreateTokenRequest{Name: "grantee-" + t.Name(), Role: "user"})

	// Grant viewer start/stop permission on this specific gameserver.
	_, err = admin.SDK().Gameservers.Update(ctx, adminGs.ID, &sdk.UpdateGameserverRequest{
		Grants: map[string][]string{viewer.ID: {"gameserver.start", "gameserver.stop"}},
	})
	require.NoError(t, err)

	// Viewer sees the granted gameserver.
	resp, err := viewer.SDK().Gameservers.List(ctx, nil)
	require.NoError(t, err)
	require.Len(t, resp.Gameservers, 1, "viewer should see the granted gameserver")
	assert.Equal(t, adminGs.ID, resp.Gameservers[0].ID)
}

// TestPermissions_CanCreate verifies that tokens without can_create can't
// create gameservers, and tokens with can_create can.
func TestPermissions_CanCreate(t *testing.T) {
	env := NewEnvSerial(t)
	admin := enableAuth(t, env)
	_ = admin

	viewer := env.NewToken(sdk.CreateTokenRequest{Name: "noviewer-" + t.Name(), Role: "user"})
	creator := env.NewToken(sdk.CreateTokenRequest{Name: "creator-" + t.Name(), Role: "user", CanCreate: true})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Viewer without can_create is forbidden.
	_, err := viewer.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name: "should-fail", GameID: env.GameID(), Env: env.GameEnv(),
	})
	require.Error(t, err, "viewer without can_create should be blocked")
	assert.True(t, sdk.IsForbidden(err), "expected 403, got %v", err)

	// Creator with can_create succeeds.
	created, err := creator.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name: "creator-gs-" + t.Name(), GameID: env.GameID(), Env: env.GameEnv(),
	})
	require.NoError(t, err, "creator with can_create should succeed")
	t.Cleanup(func() { _ = creator.SDK().Gameservers.Delete(context.Background(), created.ID) })

	// Creator sees only their own gameserver (no grants on admin's).
	resp, err := creator.SDK().Gameservers.List(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, resp.Gameservers, 1, "creator should see only owned gameserver")
}

// TestPermissions_Quota_MaxGameservers verifies that max_gameservers caps
// creation at the configured limit.
func TestPermissions_Quota_MaxGameservers(t *testing.T) {
	env := NewEnvSerial(t)
	_ = enableAuth(t, env)

	limit := 2
	creator := env.NewToken(sdk.CreateTokenRequest{
		Name:           "quota-" + t.Name(),
		Role:           "user",
		CanCreate:      true,
		MaxGameservers: &limit,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var created []string
	t.Cleanup(func() {
		for _, id := range created {
			_ = creator.SDK().Gameservers.Delete(context.Background(), id)
		}
	})

	// First two creations succeed.
	for i := 0; i < limit; i++ {
		gs, err := creator.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
			Name: "quota-ok", GameID: env.GameID(), Env: env.GameEnv(),
		})
		require.NoError(t, err, "creation %d should succeed within quota", i+1)
		created = append(created, gs.ID)
	}

	// Third hits the cap.
	_, err := creator.SDK().Gameservers.Create(ctx, &sdk.CreateGameserverRequest{
		Name: "quota-over", GameID: env.GameID(), Env: env.GameEnv(),
	})
	require.Error(t, err, "creation beyond max_gameservers should be blocked")
}

// TestPermissions_ViewerForbiddenOnClusterRoutes verifies that non-admin
// tokens can't access cluster-level routes (settings, tokens).
func TestPermissions_ViewerForbiddenOnClusterRoutes(t *testing.T) {
	env := NewEnvSerial(t)
	_ = enableAuth(t, env)

	viewer := env.NewToken(sdk.CreateTokenRequest{Name: "cluster-" + t.Name(), Role: "user"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := viewer.SDK().Settings.Get(ctx)
	assert.True(t, sdk.IsForbidden(err), "viewer should not access settings; got %v", err)

	_, err = viewer.SDK().Tokens.List(ctx)
	assert.True(t, sdk.IsForbidden(err), "viewer should not access tokens; got %v", err)
}

// TestPermissions_MeEndpoint verifies /api/me returns correct role info for
// each token type.
func TestPermissions_MeEndpoint(t *testing.T) {
	env := NewEnvSerial(t)
	admin := enableAuth(t, env)

	viewer := env.NewToken(sdk.CreateTokenRequest{Name: "me-" + t.Name(), Role: "user"})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	adminMe, err := admin.SDK().Me(ctx)
	require.NoError(t, err)
	assert.Equal(t, "admin", adminMe.Role)

	viewerMe, err := viewer.SDK().Me(ctx)
	require.NoError(t, err)
	assert.Equal(t, "user", viewerMe.Role)
}
