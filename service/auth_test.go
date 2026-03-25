package service_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestAuth_CreateAndValidateAdminToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, token, err := svc.AuthSvc.CreateAdminToken("my-admin")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)
	assert.Equal(t, "admin", token.Scope)
	assert.Equal(t, "my-admin", token.Name)

	// Validate the token
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	assert.Equal(t, token.ID, validated.ID)
	assert.Equal(t, "admin", validated.Scope)
}

func TestAuth_ValidateToken_InvalidTokenRejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	validated := svc.AuthSvc.ValidateToken("gj_bogus_token_that_does_not_exist")
	assert.Nil(t, validated, "invalid token should return nil")
}

func TestAuth_ValidateToken_ExpiredTokenRejected(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	past := time.Now().Add(-1 * time.Hour)
	rawToken, _, err := svc.AuthSvc.CreateCustomToken("expired", nil, []string{"gameserver.start"}, &past)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	assert.Nil(t, validated, "expired token should return nil")
}

func TestAuth_CustomToken_GameserverScoping(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Create two gameservers
	gs1 := &models.Gameserver{Name: "Server1", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"a"}`)}
	gs2 := &models.Gameserver{Name: "Server2", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"b"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)

	// Token scoped to gs1 only
	rawToken, _, err := svc.AuthSvc.CreateCustomToken("scoped", []string{gs1.ID}, []string{service.PermGameserverStart}, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Check permission for gs1 — should pass
	assert.True(t, service.HasPermission(validated, gs1.ID, service.PermGameserverStart))

	// Check permission for gs2 — should fail (not in scoped list)
	assert.False(t, service.HasPermission(validated, gs2.ID, service.PermGameserverStart))
}

func TestAuth_AdminToken_BypassesAllChecks(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Server", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"hello"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	rawToken := testutil.MustCreateAdminToken(t, svc)
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Admin should have permission for everything
	assert.True(t, service.HasPermission(validated, gs.ID, service.PermGameserverStart))
	assert.True(t, service.HasPermission(validated, gs.ID, service.PermGameserverDelete))
	assert.True(t, service.HasPermission(validated, "nonexistent-id", service.PermGameserverStart))
	assert.True(t, service.IsAdmin(validated))
}

func TestAuth_CustomToken_EmptyGameserverIDs_AllAccess(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Server", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"hello"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Empty gameserver_ids = all gameservers
	rawToken, _, err := svc.AuthSvc.CreateCustomToken("all-access", nil, []string{service.PermGameserverStart}, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	assert.True(t, service.HasPermission(validated, gs.ID, service.PermGameserverStart))
}

func TestAuth_CustomToken_WrongPermission(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, _, err := svc.AuthSvc.CreateCustomToken("limited", nil, []string{service.PermGameserverStart}, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Has start but not delete
	assert.True(t, service.HasPermission(validated, "any-id", service.PermGameserverStart))
	assert.False(t, service.HasPermission(validated, "any-id", service.PermGameserverDelete))
}
