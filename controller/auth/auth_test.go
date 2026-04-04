package auth_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
	rawToken, _, err := svc.AuthSvc.CreateUserToken("expired", &past, nil)
	require.NoError(t, err)

	validated := svc.AuthSvc.ValidateToken(rawToken)
	assert.Nil(t, validated, "expired token should return nil")
}

func TestAuth_HasGrantPermission(t *testing.T) {
	t.Parallel()

	// Empty grant = all permissions
	assert.True(t, auth.HasGrantPermission([]string{}, auth.PermGameserverStart))
	assert.True(t, auth.HasGrantPermission([]string{}, auth.PermGameserverDelete))

	// Specific permissions
	perms := []string{auth.PermGameserverStart, auth.PermGameserverStop}
	assert.True(t, auth.HasGrantPermission(perms, auth.PermGameserverStart))
	assert.True(t, auth.HasGrantPermission(perms, auth.PermGameserverStop))
	assert.False(t, auth.HasGrantPermission(perms, auth.PermGameserverDelete))
}

func TestAuth_AdminToken_IsAdmin(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken := testutil.MustCreateAdminToken(t, svc)
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	assert.True(t, auth.IsAdmin(validated))
}

func TestAuth_UserToken_IsNotAdmin(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, _, err := svc.AuthSvc.CreateUserToken("user", nil, nil)
	require.NoError(t, err)
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	assert.False(t, auth.IsAdmin(validated))
	assert.Equal(t, "user", validated.Role)
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

func TestAuth_HasGrantPermission_SpecificPerms(t *testing.T) {
	t.Parallel()
	perms := []string{auth.PermGameserverStart}
	assert.True(t, auth.HasGrantPermission(perms, auth.PermGameserverStart))
	assert.False(t, auth.HasGrantPermission(perms, auth.PermGameserverDelete))
}
