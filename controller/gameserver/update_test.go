package gameserver_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestUpdate_NameChange(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	update := &model.Gameserver{ID: gs.ID, Name: "New Name"}
	_, err := svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Name", fetched.Name)
}

func TestUpdate_EnvChange(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	update := &model.Gameserver{ID: gs.ID, Env: model.Env{"REQUIRED_VAR": "updated", "SERVER_NAME": "My Server"}}
	_, err := svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", fetched.Env["REQUIRED_VAR"])
	assert.Equal(t, "My Server", fetched.Env["SERVER_NAME"])
}

func TestUpdate_NonAdminBlockedFromResources(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// Create a non-admin token and put it in context
	rawToken, _, err := svc.AuthSvc.CreateUserToken("limited", nil, []string{auth.PermGameserverConfigureEnv}, nil, nil)
	require.NoError(t, err)
	token := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, token)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	// Try to change memory — should be blocked
	update := &model.Gameserver{ID: gs.ID, MemoryLimitMB: 4096}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing permission")
}

func TestUpdate_AdminCanChangeResources(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// Admin token in context
	rawToken := testutil.MustCreateAdminToken(t, svc)
	token := svc.AuthSvc.ValidateToken(rawToken)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	update := &model.Gameserver{ID: gs.ID, MemoryLimitMB: 4096}
	_, err := svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, 4096, fetched.MemoryLimitMB)
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	update := &model.Gameserver{ID: "nonexistent", Name: "Whatever"}
	_, err := svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
