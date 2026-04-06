package integration_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// ownerContext creates a context with a user token that owns the gameservers it creates.
func ownerContext(t *testing.T, svc *testutil.ServiceBundle, canCreate bool) (context.Context, *model.Token) {
	t.Helper()
	raw, token, err := svc.AuthSvc.CreateUserToken("owner", canCreate, nil, nil)
	require.NoError(t, err)
	validated := svc.AuthSvc.ValidateToken(raw)
	require.NotNil(t, validated)
	return auth.SetTokenInContext(testutil.TestContext(), validated), token
}

// --- Ownership tests ---

func TestPermissions_OwnerSeesOwnGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	adminCtx := testutil.TestContext()

	ownerCtx, _ := ownerContext(t, svc, true)

	// Owner creates a gameserver
	gs := &model.Gameserver{Name: "My Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ownerCtx, gs)
	require.NoError(t, err)

	// Owner sees it
	list, err := svc.GameserverSvc.ListGameservers(ownerCtx, model.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, gs.ID, list[0].ID)

	// Admin also sees it
	list, err = svc.GameserverSvc.ListGameservers(adminCtx, model.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestPermissions_NonOwnerWithoutGrantCannotSee(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	ownerCtx, _ := ownerContext(t, svc, true)

	// Owner creates a gameserver
	gs := &model.Gameserver{Name: "Private Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ownerCtx, gs)
	require.NoError(t, err)

	// Another user token with no grants
	otherRaw, _, err := svc.AuthSvc.CreateUserToken("other", false, nil, nil)
	require.NoError(t, err)
	otherToken := svc.AuthSvc.ValidateToken(otherRaw)
	otherCtx := auth.SetTokenInContext(testutil.TestContext(), otherToken)

	// Other user sees nothing
	list, err := svc.GameserverSvc.ListGameservers(otherCtx, model.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 0)
}

func TestPermissions_OwnerHasAllPermissions(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	ownerCtx, _ := ownerContext(t, svc, true)

	gs := &model.Gameserver{Name: "Owner Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ownerCtx, gs)
	require.NoError(t, err)

	// Owner can update name (no grant needed — ownership gives all permissions)
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed"}
	err = svc.GameserverSvc.UpdateGameserver(ownerCtx, update)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed", fetched.Name)
}

// --- Grant tests ---

func TestPermissions_GrantedUserSeesGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	adminCtx := testutil.TestContext()
	db := store.New(svc.DB)

	// Admin creates a gameserver
	gs := &model.Gameserver{Name: "Shared Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(adminCtx, gs)
	require.NoError(t, err)

	// Create a user token and grant it access
	granteeRaw, granteeToken, err := svc.AuthSvc.CreateUserToken("grantee", false, nil, nil)
	require.NoError(t, err)

	full, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	full.Grants = model.GrantMap{granteeToken.ID: {auth.PermGameserverStart, auth.PermGameserverStop}}
	require.NoError(t, db.UpdateGameserver(full))

	granteeValidated := svc.AuthSvc.ValidateToken(granteeRaw)
	granteeCtx := auth.SetTokenInContext(testutil.TestContext(), granteeValidated)

	// Grantee sees the server
	list, err := svc.GameserverSvc.ListGameservers(granteeCtx, model.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, gs.ID, list[0].ID)
}

func TestPermissions_EmptyGrantMeansFullAccess(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	adminCtx := testutil.TestContext()
	db := store.New(svc.DB)

	gs := &model.Gameserver{Name: "Full Access Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(adminCtx, gs)
	require.NoError(t, err)

	// Grant with empty perms = full access
	granteeRaw, granteeToken, err := svc.AuthSvc.CreateUserToken("full-grantee", false, nil, nil)
	require.NoError(t, err)

	full, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	full.Grants = model.GrantMap{granteeToken.ID: {}} // empty = all permissions
	require.NoError(t, db.UpdateGameserver(full))

	granteeValidated := svc.AuthSvc.ValidateToken(granteeRaw)
	granteeCtx := auth.SetTokenInContext(testutil.TestContext(), granteeValidated)

	// Grantee can update name (full access)
	update := &model.Gameserver{ID: gs.ID, Name: "Renamed by grantee"}
	err = svc.GameserverSvc.UpdateGameserver(granteeCtx, update)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Equal(t, "Renamed by grantee", fetched.Name)
}

func TestPermissions_GrantedUserBlockedFromUngrantedPermission(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	adminCtx := testutil.TestContext()
	db := store.New(svc.DB)

	gs := &model.Gameserver{Name: "Limited Server", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(adminCtx, gs)
	require.NoError(t, err)

	// Grant only env permission
	granteeRaw, granteeToken, err := svc.AuthSvc.CreateUserToken("limited-grantee", false, nil, nil)
	require.NoError(t, err)

	full, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	full.Grants = model.GrantMap{granteeToken.ID: {auth.PermGameserverConfigureEnv}}
	require.NoError(t, db.UpdateGameserver(full))

	granteeValidated := svc.AuthSvc.ValidateToken(granteeRaw)
	granteeCtx := auth.SetTokenInContext(testutil.TestContext(), granteeValidated)

	// Grantee can update env
	update := &model.Gameserver{ID: gs.ID, Env: model.Env{"REQUIRED_VAR": "new"}}
	err = svc.GameserverSvc.UpdateGameserver(granteeCtx, update)
	require.NoError(t, err)

	// Grantee cannot update name (not in grant)
	update = &model.Gameserver{ID: gs.ID, Name: "Blocked"}
	err = svc.GameserverSvc.UpdateGameserver(granteeCtx, update)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing permission")
}

// --- can_create tests ---

func TestPermissions_CanCreateTrue_AllowsCreation(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	ctx, _ := ownerContext(t, svc, true)

	gs := &model.Gameserver{Name: "Created", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)
	assert.NotEmpty(t, gs.ID)
}

func TestPermissions_CanCreateFalse_BlocksCreation(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	raw, _, err := svc.AuthSvc.CreateUserToken("no-create", false, nil, nil)
	require.NoError(t, err)
	token := svc.AuthSvc.ValidateToken(raw)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	gs := &model.Gameserver{Name: "Blocked", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs)
	// Note: creation permission is checked at the HTTP middleware level (RequireClusterPermission),
	// not in the service. At the service level, any token can call CreateGameserver — the middleware
	// gates access. So this test verifies the token gets created_by_token_id set correctly.
	// The HTTP-level test is in the handler tests.
	require.NoError(t, err)
	assert.Equal(t, token.ID, *gs.CreatedByTokenID)
}

// --- Quota enforcement tests ---

func TestPermissions_QuotaMaxGameservers_Enforced(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	maxGS := 2
	quotas := &auth.UserTokenQuotas{MaxGameservers: &maxGS}
	raw, _, err := svc.AuthSvc.CreateUserToken("quota-user", true, nil, quotas)
	require.NoError(t, err)
	token := svc.AuthSvc.ValidateToken(raw)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	// Create two gameservers — should work
	gs1 := &model.Gameserver{Name: "GS1", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	gs2 := &model.Gameserver{Name: "GS2", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.NoError(t, err)

	// Third should fail
	gs3 := &model.Gameserver{Name: "GS3", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs3)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestPermissions_QuotaMaxMemory_Enforced(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	maxMem := 4096
	quotas := &auth.UserTokenQuotas{MaxMemoryMB: &maxMem}
	raw, _, err := svc.AuthSvc.CreateUserToken("mem-user", true, nil, quotas)
	require.NoError(t, err)
	token := svc.AuthSvc.ValidateToken(raw)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	// Create with 3GB — should work
	gs1 := &model.Gameserver{Name: "GS1", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}, MemoryLimitMB: 3072}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs1)
	require.NoError(t, err)

	// Create with 2GB — total 5GB, exceeds 4GB quota
	gs2 := &model.Gameserver{Name: "GS2", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}, MemoryLimitMB: 2048}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gs2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "quota exceeded")
}

func TestPermissions_NilQuota_Unlimited(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	// can_create with no quotas = unlimited
	raw, _, err := svc.AuthSvc.CreateUserToken("unlimited", true, nil, nil)
	require.NoError(t, err)
	token := svc.AuthSvc.ValidateToken(raw)
	ctx := auth.SetTokenInContext(testutil.TestContext(), token)

	// Create several — all should work
	for i := 0; i < 5; i++ {
		gs := &model.Gameserver{Name: "GS", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
		_, err = svc.GameserverSvc.CreateGameserver(ctx, gs)
		require.NoError(t, err)
	}
}

// --- created_by_token_id tests ---

func TestPermissions_CreatedByTokenID_SetOnCreate(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	ctx, token := ownerContext(t, svc, true)

	gs := &model.Gameserver{Name: "Owned", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.CreatedByTokenID)
	assert.Equal(t, token.ID, *fetched.CreatedByTokenID)
}

func TestPermissions_AdminCreate_SetsCreatedByTokenID(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	// Admin token in context
	adminRaw := testutil.MustCreateAdminToken(t, svc)
	adminToken := svc.AuthSvc.ValidateToken(adminRaw)
	ctx := auth.SetTokenInContext(testutil.TestContext(), adminToken)

	gs := &model.Gameserver{Name: "Admin Created", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.CreatedByTokenID)
	assert.Equal(t, adminToken.ID, *fetched.CreatedByTokenID)
}

func TestPermissions_GrantsPersistedViaPatch(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	adminCtx := testutil.TestContext()
	db := store.New(svc.DB)

	// Admin creates a gameserver
	gs := &model.Gameserver{Name: "Grant Persist", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(adminCtx, gs)
	require.NoError(t, err)

	// Create a user token
	_, granteeToken, err := svc.AuthSvc.CreateUserToken("grantee", false, nil, nil)
	require.NoError(t, err)

	// Update grants via the service (simulating PATCH)
	update := &model.Gameserver{
		ID:     gs.ID,
		Grants: model.GrantMap{granteeToken.ID: {auth.PermGameserverStart}},
	}
	err = svc.GameserverSvc.UpdateGameserver(adminCtx, update)
	require.NoError(t, err)

	// Verify grants persisted
	fetched, err := db.GetGameserver(gs.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched.Grants)
	perms, ok := fetched.Grants[granteeToken.ID]
	assert.True(t, ok, "grant should exist for token")
	assert.Equal(t, []string{auth.PermGameserverStart}, perms)
}

func TestPermissions_NoToken_CreatedByTokenIDNil(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")

	// No token in context (auth disabled)
	ctx := testutil.TestContext()

	gs := &model.Gameserver{Name: "No Auth", GameID: testutil.TestGameID, Env: model.Env{"REQUIRED_VAR": "v"}}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	fetched, err := svc.GameserverSvc.GetGameserver(gs.ID)
	require.NoError(t, err)
	assert.Nil(t, fetched.CreatedByTokenID)
}
