package gameserver_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Verify it was set
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	require.True(t, fetched.CPUEnforced)

	// Update just the name — cpu_enforced should stay true
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	after, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	assert.True(t, after.CPUEnforced, "cpu_enforced should not be cleared by a name-only update")
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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Mark as installed
	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.Installed = true
	store.New(svc.DB).UpdateGameserver(fetched)

	// Change the INSTALL_TRIGGER env var — should clear installed flag
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "beta"},
	}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	after, _ := svc.GameserverSvc.GetGameserver(gs.ID)
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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	fetched, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	fetched.Installed = true
	store.New(svc.DB).UpdateGameserver(fetched)

	// Update env with SAME value for INSTALL_TRIGGER — should NOT clear installed
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"REQUIRED_VAR": "v", "INSTALL_TRIGGER": "stable"},
	}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	after, _ := svc.GameserverSvc.GetGameserver(gs.ID)
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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Update just name — memory and CPU should stay unchanged
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	after, _ := svc.GameserverSvc.GetGameserver(gs.ID)
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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	original, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	originalPorts := original.Ports

	// Update name only — ports should be untouched
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.NoError(t, err)

	after, _ := svc.GameserverSvc.GetGameserver(gs.ID)
	assert.Equal(t, originalPorts, after.Ports, "ports should not change on name-only update")
}

func TestUpdateMerge_AutoMigrationTriggered(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1", testutil.WithMaxMemoryMB(2048))
	testutil.RegisterFakeWorker(t, svc, "worker-2", testutil.WithMaxMemoryMB(8192))
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:          "Auto Migrate",
		GameID:        testutil.TestGameID,
		MemoryLimitMB: 1024,
		NodeID:        testutil.StrPtr("worker-1"),
		Env:           model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Increase memory beyond worker-1's capacity — should trigger auto-migration
	adminToken := testutil.MustCreateAdminToken(t, svc)
	token := svc.AuthSvc.ValidateToken(adminToken)
	adminCtx := auth.SetTokenInContext(ctx, token)

	update := &model.Gameserver{ID: gs.ID, MemoryLimitMB: 4096}
	migrationTriggered, err := svc.GameserverSvc.UpdateGameserver(adminCtx, update)
	require.NoError(t, err)
	assert.True(t, migrationTriggered, "should trigger auto-migration when resources exceed current node")
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
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	// Update env without the required var — should fail
	update := &model.Gameserver{
		ID:  gs.ID,
		Env: model.Env{"SERVER_NAME": "new name"},
	}
	_, err = svc.GameserverSvc.UpdateGameserver(ctx, update)
	require.Error(t, err, "updating env without required var should fail validation")
}
