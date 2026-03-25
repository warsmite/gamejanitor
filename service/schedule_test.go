package service_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestSchedule_Create_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Sched Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	sched := &models.Schedule{
		GameserverID: gs.ID,
		Name:         "daily-restart",
		Type:         "restart",
		CronExpr:     "0 4 * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
	require.NoError(t, err)
	assert.NotEmpty(t, sched.ID)
}

func TestSchedule_Create_InvalidCron(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Sched Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	sched := &models.Schedule{
		GameserverID: gs.ID,
		Name:         "bad-cron",
		Type:         "restart",
		CronExpr:     "not a cron expression",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
	require.Error(t, err)
}

func TestSchedule_List_ByGameserver(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "List Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	for _, name := range []string{"sched-1", "sched-2"} {
		sched := &models.Schedule{
			GameserverID: gs.ID, Name: name, Type: "restart",
			CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true,
		}
		require.NoError(t, svc.ScheduleSvc.CreateSchedule(ctx, sched))
	}

	list, err := svc.ScheduleSvc.ListSchedules(gs.ID)
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestSchedule_Delete_HappyPath(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Del Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	sched := &models.Schedule{
		GameserverID: gs.ID, Name: "to-delete", Type: "restart",
		CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true,
	}
	require.NoError(t, svc.ScheduleSvc.CreateSchedule(ctx, sched))

	err = svc.ScheduleSvc.DeleteSchedule(ctx, gs.ID, sched.ID)
	require.NoError(t, err)

	fetched, err := svc.ScheduleSvc.GetSchedule(gs.ID, sched.ID)
	require.Error(t, err)
	assert.Nil(t, fetched)
}

func TestSchedule_Create_InvalidType(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &models.Gameserver{Name: "Type Host", GameID: testutil.TestGameID, Env: []byte(`{"REQUIRED_VAR":"v"}`)}
	_, err := svc.GameserverSvc.CreateGameserver(ctx, gs)
	require.NoError(t, err)

	sched := &models.Schedule{
		GameserverID: gs.ID, Name: "bad-type", Type: "invalid_type",
		CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true,
	}
	err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
	require.Error(t, err)
}
