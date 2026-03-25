package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/testutil"
)

// scopedContext creates a context with a custom token scoped to the given gameserver IDs.
func scopedContext(t *testing.T, svc *testutil.ServiceBundle, perms []string, gsIDs []string) context.Context {
	t.Helper()
	rawToken := testutil.MustCreateCustomToken(t, svc, perms, gsIDs)
	token := svc.AuthSvc.ValidateToken(rawToken)
	require.NotNil(t, token)
	return service.SetTokenInContext(testutil.TestContext(), token)
}

func TestScoping_Backup_CrossAccess_Blocked(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	// Create two gameservers
	gsA := &models.Gameserver{Name: "GS-A", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gsA)
	require.NoError(t, err)

	gsB := &models.Gameserver{Name: "GS-B", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gsB)
	require.NoError(t, err)

	// Create backup on GS-A
	backup, err := svc.BackupSvc.CreateBackup(ctx, gsA.ID, "test-backup")
	require.NoError(t, err)

	// Wait for backup to complete (async goroutine)
	waitForBackupCompletion(t, svc, backup.ID)

	// GetBackup with correct gameserver succeeds
	got, err := svc.BackupSvc.GetBackup(gsA.ID, backup.ID)
	require.NoError(t, err)
	assert.Equal(t, backup.ID, got.ID)

	// GetBackup with wrong gameserver returns not found
	got, err = svc.BackupSvc.GetBackup(gsB.ID, backup.ID)
	require.Error(t, err)
	assert.Nil(t, got)

	// DeleteBackup with wrong gameserver returns not found
	err = svc.BackupSvc.DeleteBackup(ctx, gsB.ID, backup.ID)
	require.Error(t, err)

	// Original backup still exists
	got, err = svc.BackupSvc.GetBackup(gsA.ID, backup.ID)
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestScoping_Schedule_CrossAccess_Blocked(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gsA := &models.Gameserver{Name: "GS-A", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gsA)
	require.NoError(t, err)

	gsB := &models.Gameserver{Name: "GS-B", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gsB)
	require.NoError(t, err)

	// Create schedule on GS-A
	sched := &models.Schedule{
		GameserverID: gsA.ID, Name: "test-sched", Type: "restart",
		CronExpr: "0 0 * * *", Payload: []byte(`{}`), Enabled: true,
	}
	require.NoError(t, svc.ScheduleSvc.CreateSchedule(ctx, sched))

	// GetSchedule with correct gameserver succeeds
	got, err := svc.ScheduleSvc.GetSchedule(gsA.ID, sched.ID)
	require.NoError(t, err)
	assert.Equal(t, sched.ID, got.ID)

	// GetSchedule with wrong gameserver returns not found
	got, err = svc.ScheduleSvc.GetSchedule(gsB.ID, sched.ID)
	require.Error(t, err)
	assert.Nil(t, got)

	// DeleteSchedule with wrong gameserver returns not found
	err = svc.ScheduleSvc.DeleteSchedule(ctx, gsB.ID, sched.ID)
	require.Error(t, err)

	// Original schedule still exists
	got, err = svc.ScheduleSvc.GetSchedule(gsA.ID, sched.ID)
	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestScoping_ListGameservers_TokenScoped(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gsA := &models.Gameserver{Name: "GS-A", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gsA)
	require.NoError(t, err)

	gsB := &models.Gameserver{Name: "GS-B", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gsB)
	require.NoError(t, err)

	// Scoped token for GS-A only
	scopedCtx := scopedContext(t, svc, []string{"gameserver.start"}, []string{gsA.ID})

	// List with scoped context — only sees GS-A
	list, err := svc.GameserverSvc.ListGameservers(scopedCtx, models.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, gsA.ID, list[0].ID)

	// List with unscoped context — sees both
	list, err = svc.GameserverSvc.ListGameservers(ctx, models.GameserverFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestScoping_ListGameservers_IDsIntersectsTokenScope(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gsA := &models.Gameserver{Name: "GS-A", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gsA)
	require.NoError(t, err)

	gsB := &models.Gameserver{Name: "GS-B", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err = svc.GameserverSvc.CreateGameserver(ctx, gsB)
	require.NoError(t, err)

	// Token scoped to GS-A, but requesting GS-B via ?ids= — should get empty
	scopedCtx := scopedContext(t, svc, []string{"gameserver.start"}, []string{gsA.ID})
	list, err := svc.GameserverSvc.ListGameservers(scopedCtx, models.GameserverFilter{IDs: []string{gsB.ID}})
	require.NoError(t, err)
	assert.Len(t, list, 0)

	// Token scoped to GS-A, requesting GS-A via ?ids= — should get GS-A
	list, err = svc.GameserverSvc.ListGameservers(scopedCtx, models.GameserverFilter{IDs: []string{gsA.ID}})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	assert.Equal(t, gsA.ID, list[0].ID)
}

func TestScoping_IntersectIDs(t *testing.T) {
	// nil allowed = all-access, returns requested
	result := service.ExportIntersectIDs(nil, nil)
	assert.Nil(t, result)

	result = service.ExportIntersectIDs([]string{"a", "b"}, nil)
	assert.Equal(t, []string{"a", "b"}, result)

	// nil requested = no filter, returns allowed
	result = service.ExportIntersectIDs(nil, []string{"x", "y"})
	assert.Equal(t, []string{"x", "y"}, result)

	// Intersection
	result = service.ExportIntersectIDs([]string{"a", "b", "c"}, []string{"b", "c", "d"})
	assert.Equal(t, []string{"b", "c"}, result)

	// Disjoint — returns empty slice (not nil), meaning "no results"
	result = service.ExportIntersectIDs([]string{"a"}, []string{"b"})
	assert.Empty(t, result)
	assert.NotNil(t, result)
}
