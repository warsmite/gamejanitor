package gameserver_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestUpdate_NameChange(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := testutil.CreateTestGameserver(t, svc)

	update := &model.Gameserver{ID: gs.ID, Name: "New Name"}
	err := svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.Manager.GetGameserver(gs.ID)
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
	err := svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "updated", fetched.Env["REQUIRED_VAR"])
	assert.Equal(t, "My Server", fetched.Env["SERVER_NAME"])
}

func TestUpdate_NonAdminBlockedFromResources(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	gs := testutil.CreateTestGameserver(t, svc)

	// Create a non-admin token with env-only access (not resources)
	rawToken, token, err := svc.AuthSvc.CreateUserToken("limited", false, nil, nil)
	require.NoError(t, err)

	// Grant env-only permission on this gameserver
	db := store.New(svc.DB)
	full, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	full.Grants = model.GrantMap{token.ID: {auth.PermGameserverConfigureEnv}}
	require.NoError(t, db.UpdateGameserver(full))

	validated := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, validated)
	ctx := auth.SetTokenInContext(testutil.TestContext(), validated)

	// Try to change memory — should be blocked (has env perm, not resources)
	update := &model.Gameserver{ID: gs.ID, MemoryLimitMB: 4096}
	err = svc.Manager.UpdateConfig(ctx, update)
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
	err := svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, 4096, fetched.MemoryLimitMB)
}

func TestUpdate_NotFound(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	update := &model.Gameserver{ID: "nonexistent", Name: "Whatever"}
	err := svc.Manager.UpdateConfig(ctx, update)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestUpdateMerge_EnvTriggersReinstall(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Reinstall Test",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "stable"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Mark as installed
	db := store.New(svc.DB)
	fetched, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	fetched.Installed = true
	require.NoError(t, db.UpdateGameserver(fetched))

	// Change the INSTALL_TRIGGER env var — should clear installed flag
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "beta"},
	}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	after, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.False(t, after.Installed, "changing a triggers_install env var should clear installed flag")
}

func TestUpdateMerge_EnvNoChangeDoesNotClearInstalled(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "No Change",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "stable"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	db := store.New(svc.DB)
	fetched, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	fetched.Installed = true
	require.NoError(t, db.UpdateGameserver(fetched))

	// Update env with SAME value for INSTALL_TRIGGER — should NOT clear installed
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "stable"},
	}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	after, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.True(t, after.Installed, "same env values should not trigger reinstall")
}

func TestUpdateMerge_ZeroValueFieldsNotOverwritten(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Zero Guard",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 4096,
		CPULimit:      2.0,
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Update just name — memory and CPU should stay unchanged
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	after, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, 4096, after.MemoryLimitMB, "memory should not be cleared by zero value")
	assert.Equal(t, 2.0, after.CPULimit, "CPU should not be cleared by zero value")
}

func TestUpdateMerge_PortsNilNotOverwritten(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Ports Guard",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	original, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	originalPorts := original.Ports

	// Update name only — ports should be untouched
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	after, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, originalPorts, after.Ports, "ports should not change on name-only update")
}

func TestUpdateMerge_EnvValidationStillEnforced(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "Validate Env",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Update env without the required var — should fail
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"SERVER_NAME": "new name"},
	}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.Error(t, err, "updating env without required var should fail validation")
}

func TestUpdateMerge_CPUEnforcedOverwrittenOnEveryUpdate(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Create with cpu_enforced = true via admin
	gs := &model.Gameserver{
		Name:        "CPU Enforced",
		GameID:      testutil.TestGameID,
		CPULimit:    2.0,
		CPUEnforced: true,
		Env:         model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Verify it was set
	fetched, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.True(t, fetched.CPUEnforced)

	// Update just the name — cpu_enforced should stay true
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	err = svc.Manager.UpdateConfig(ctx, update)
	require.NoError(t, err)

	after, err := svc.Manager.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.True(t, after.CPUEnforced, "cpu_enforced should not be cleared by a name-only update")
}
