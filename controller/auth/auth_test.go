package auth_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestAuth_CreateAndValidateAdminToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, token, err := svc.AuthSvc.CreateAdminToken("my-admin")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)
	assert.Equal(t, "admin", token.Role)
	assert.Equal(t, "my-admin", token.Name)

	// Validate the token
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	assert.Equal(t, token.ID, validated.ID)
	assert.Equal(t, "admin", validated.Role)
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
	rawToken, _, err := svc.AuthSvc.CreateUserToken("expired", nil, []string{"gameserver.start"}, &past, nil)
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
	gs1 := &model.Gameserver{Name: "Server1", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "a"}}
	gs2 := &model.Gameserver{Name: "Server2", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "b"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)

	// Token scoped to gs1 only
	rawToken, _, err := svc.AuthSvc.CreateUserToken("scoped", []string{gs1.ID}, []string{auth.PermGameserverStart}, nil, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Check permission for gs1 — should pass
	assert.True(t, auth.HasPermission(validated, gs1.ID, auth.PermGameserverStart))

	// Check permission for gs2 — should fail (not in scoped list)
	assert.False(t, auth.HasPermission(validated, gs2.ID, auth.PermGameserverStart))
}

func TestAuth_AdminToken_BypassesAllChecks(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "hello"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	rawToken := testutil.MustCreateAdminToken(t, svc)
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Admin should have permission for everything
	assert.True(t, auth.HasPermission(validated, gs.ID, auth.PermGameserverStart))
	assert.True(t, auth.HasPermission(validated, gs.ID, auth.PermGameserverDelete))
	assert.True(t, auth.HasPermission(validated, "nonexistent-id", auth.PermGameserverStart))
	assert.True(t, auth.IsAdmin(validated))
}

func TestAuth_CustomToken_EmptyGameserverIDs_AllAccess(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "hello"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Empty gameserver_ids = no granted access (ownership-based only)
	rawToken, _, err := svc.AuthSvc.CreateUserToken("no-grants", nil, []string{auth.PermGameserverStart}, nil, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	assert.False(t, auth.HasPermission(validated, gs.ID, auth.PermGameserverStart),
		"empty gameserver_ids should not grant access — ownership is checked separately")
}

func TestAuth_CustomToken_InvalidGameserverID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	_, _, err := svc.AuthSvc.CreateUserToken("bad-scope", []string{"nonexistent-gs"}, []string{auth.PermGameserverStart}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestAuth_CreateWorkerToken_Idempotent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken1, token1, err := svc.AuthSvc.CreateWorkerToken("my-worker")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken1, "first create should return raw token")

	// Second create with same name returns existing, no raw token
	rawToken2, token2, err := svc.AuthSvc.CreateWorkerToken("my-worker")
	require.NoError(t, err)
	assert.Empty(t, rawToken2, "second create should not return raw token")
	assert.Equal(t, token1.ID, token2.ID, "should return same token")
}

func TestAuth_CreateAdminToken_Idempotent(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken1, token1, err := svc.AuthSvc.CreateAdminToken("my-admin")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken1)

	rawToken2, token2, err := svc.AuthSvc.CreateAdminToken("my-admin")
	require.NoError(t, err)
	assert.Empty(t, rawToken2, "second create should not return raw token")
	assert.Equal(t, token1.ID, token2.ID)
}

func TestAuth_RotateWorkerToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	// Rotate with no existing token creates a new one
	rawToken1, token1, err := svc.AuthSvc.RotateWorkerToken("my-worker")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken1)

	// Validate the first token works
	validated := svc.AuthSvc.ValidateToken(rawToken1)
	require.NotNil(t, validated)
	assert.Equal(t, token1.ID, validated.ID)

	// Rotate replaces it
	rawToken2, token2, err := svc.AuthSvc.RotateWorkerToken("my-worker")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken2)
	assert.NotEqual(t, token1.ID, token2.ID, "should be a new token")
	assert.NotEqual(t, rawToken1, rawToken2, "should have new raw token")

	// Old token no longer valid
	assert.Nil(t, svc.AuthSvc.ValidateToken(rawToken1), "old token should be invalidated")

	// New token works
	validated2 := svc.AuthSvc.ValidateToken(rawToken2)
	require.NotNil(t, validated2)
	assert.Equal(t, token2.ID, validated2.ID)
}

func TestAuth_RotateAdminToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken1, _, err := svc.AuthSvc.RotateAdminToken("my-admin")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken1)

	rawToken2, _, err := svc.AuthSvc.RotateAdminToken("my-admin")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken2)
	assert.NotEqual(t, rawToken1, rawToken2)

	// Old invalidated, new works
	assert.Nil(t, svc.AuthSvc.ValidateToken(rawToken1))
	assert.NotNil(t, svc.AuthSvc.ValidateToken(rawToken2))
}

func TestAuth_CreateWorkerToken_DifferentNames(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken1, _, err := svc.AuthSvc.CreateWorkerToken("worker-a")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken1)

	rawToken2, _, err := svc.AuthSvc.CreateWorkerToken("worker-b")
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken2, "different names should both get raw tokens")
	assert.NotEqual(t, rawToken1, rawToken2)
}

func TestAuth_UserToken_WrongPermission(t *testing.T) {
	t.Parallel()

	// Test HasPermission directly — no store needed
	token := &model.Token{
		Role:          "user",
		GameserverIDs: model.StringSlice{"gs-1"},
		Permissions:   model.StringSlice{auth.PermGameserverStart},
	}

	assert.True(t, auth.HasPermission(token, "gs-1", auth.PermGameserverStart))
	assert.False(t, auth.HasPermission(token, "gs-1", auth.PermGameserverDelete))
	assert.False(t, auth.HasPermission(token, "gs-2", auth.PermGameserverStart))
}
