package service_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/testutil"
)

func TestAuth_ListTokens(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	svc.AuthSvc.CreateAdminToken("admin-1")
	svc.AuthSvc.CreateAdminToken("admin-2")

	tokens, err := svc.AuthSvc.ListTokens()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(tokens), 2)
}

func TestAuth_ListTokensByScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	svc.AuthSvc.CreateAdminToken("admin-1")
	svc.AuthSvc.CreateWorkerToken("worker-1")

	adminTokens, err := svc.AuthSvc.ListTokensByScope("admin")
	require.NoError(t, err)
	assert.Len(t, adminTokens, 1)
	assert.Equal(t, "admin", adminTokens[0].Scope)

	workerTokens, err := svc.AuthSvc.ListTokensByScope("worker")
	require.NoError(t, err)
	assert.Len(t, workerTokens, 1)
	assert.Equal(t, "worker", workerTokens[0].Scope)
}

func TestAuth_DeleteToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, token, err := svc.AuthSvc.CreateAdminToken("to-delete")
	require.NoError(t, err)
	require.NotEmpty(t, rawToken)

	// Validate it works
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)

	// Delete
	require.NoError(t, svc.AuthSvc.DeleteToken(token.ID))

	// Should no longer validate
	assert.Nil(t, svc.AuthSvc.ValidateToken(rawToken))
}

func TestAuth_DeleteToken_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	err := svc.AuthSvc.DeleteToken("nonexistent")
	require.Error(t, err)
}

func TestAuth_GetToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	_, token, err := svc.AuthSvc.CreateAdminToken("get-test")
	require.NoError(t, err)

	fetched, err := svc.AuthSvc.GetToken(token.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	assert.Equal(t, "get-test", fetched.Name)
	assert.Equal(t, "admin", fetched.Scope)
}

func TestAuth_GetToken_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	fetched, err := svc.AuthSvc.GetToken("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, fetched)
}

func TestAuth_GenerateAdminToken(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	rawToken, err := svc.AuthSvc.GenerateAdminToken()
	require.NoError(t, err)
	assert.NotEmpty(t, rawToken)

	// Should be a valid admin token
	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	assert.Equal(t, "admin", validated.Scope)
}

func TestAuth_IsWorkerTokenValid(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)

	_, token, err := svc.AuthSvc.CreateWorkerToken("test-worker")
	require.NoError(t, err)

	assert.True(t, svc.AuthSvc.IsWorkerTokenValid(token.ID))
	assert.False(t, svc.AuthSvc.IsWorkerTokenValid("nonexistent"))
}
