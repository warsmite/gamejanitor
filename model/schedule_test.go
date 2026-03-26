package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func TestSchedule_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-sched", "Sched Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	nextRun := time.Now().Add(time.Hour).Truncate(time.Second)
	s := &model.Schedule{
		ID:           "sched-1",
		GameserverID: "gs-sched",
		Name:         "Hourly Restart",
		Type:         "restart",
		CronExpr:     "0 * * * *",
		Payload:      json.RawMessage(`{"graceful":true}`),
		Enabled:      true,
		OneShot:      false,
		NextRun:      &nextRun,
	}
	require.NoError(t, model.CreateSchedule(db, s))
	assert.False(t, s.CreatedAt.IsZero())

	got, err := model.GetSchedule(db, "sched-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "sched-1", got.ID)
	assert.Equal(t, "gs-sched", got.GameserverID)
	assert.Equal(t, "Hourly Restart", got.Name)
	assert.Equal(t, "restart", got.Type)
	assert.Equal(t, "0 * * * *", got.CronExpr)
	assert.True(t, got.Enabled)
	assert.False(t, got.OneShot)
	assert.Nil(t, got.LastRun)
	require.NotNil(t, got.NextRun)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(got.Payload, &payload))
	assert.Equal(t, true, payload["graceful"])
}

func TestSchedule_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	got, err := model.GetSchedule(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestSchedule_Update(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-su", "Update Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	s := &model.Schedule{
		ID:           "sched-upd",
		GameserverID: "gs-su",
		Name:         "Original",
		Type:         "backup",
		CronExpr:     "0 0 * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	require.NoError(t, model.CreateSchedule(db, s))

	now := time.Now().Truncate(time.Second)
	s.Name = "Updated Schedule"
	s.Enabled = false
	s.LastRun = &now
	require.NoError(t, model.UpdateSchedule(db, s))

	got, err := model.GetSchedule(db, "sched-upd")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Updated Schedule", got.Name)
	assert.False(t, got.Enabled)
	require.NotNil(t, got.LastRun)
}

func TestSchedule_UpdateNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	s := &model.Schedule{
		ID:       "nonexistent",
		Name:     "Ghost",
		Type:     "backup",
		CronExpr: "0 0 * * *",
		Payload:  json.RawMessage(`{}`),
	}
	err := model.UpdateSchedule(db, s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSchedule_ListByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-sl", "List Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	s1 := &model.Schedule{ID: "sched-a", GameserverID: "gs-sl", Name: "Alpha", Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	s2 := &model.Schedule{ID: "sched-b", GameserverID: "gs-sl", Name: "Bravo", Type: "restart", CronExpr: "0 6 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	require.NoError(t, model.CreateSchedule(db, s1))
	require.NoError(t, model.CreateSchedule(db, s2))

	list, err := model.ListSchedules(db, "gs-sl")
	require.NoError(t, err)
	assert.Len(t, list, 2)
	// Ordered by name
	assert.Equal(t, "Alpha", list[0].Name)
	assert.Equal(t, "Bravo", list[1].Name)
}

func TestSchedule_ListByGameserver_Empty(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	list, err := model.ListSchedules(db, "gs-nonexistent")
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSchedule_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-sd", "Del Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	s := &model.Schedule{ID: "sched-del", GameserverID: "gs-sd", Name: "Delete Me", Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	require.NoError(t, model.CreateSchedule(db, s))

	require.NoError(t, model.DeleteSchedule(db, "sched-del"))

	got, err := model.GetSchedule(db, "sched-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestSchedule_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	err := model.DeleteSchedule(db, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestSchedule_DeleteByGameserver(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-sbd", "Bulk Del Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	for _, id := range []string{"sched-1", "sched-2"} {
		s := &model.Schedule{ID: id, GameserverID: "gs-sbd", Name: id, Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
		require.NoError(t, model.CreateSchedule(db, s))
	}

	require.NoError(t, model.DeleteSchedulesByGameserver(db, "gs-sbd"))

	list, err := model.ListSchedules(db, "gs-sbd")
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestSchedule_CascadeOnGameserverDelete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	gs := newGameserver("gs-scas", "Cascade Host", "minecraft-java", nil)
	require.NoError(t, model.CreateGameserver(db, gs))

	s := &model.Schedule{ID: "sched-cas", GameserverID: "gs-scas", Name: "Cascade Schedule", Type: "backup", CronExpr: "0 0 * * *", Payload: json.RawMessage(`{}`), Enabled: true}
	require.NoError(t, model.CreateSchedule(db, s))

	// Must delete schedules before gameserver (no ON DELETE CASCADE).
	require.NoError(t, model.DeleteSchedulesByGameserver(db, "gs-scas"))
	require.NoError(t, model.DeleteGameserver(db, "gs-scas"))

	got, err := model.GetSchedule(db, "sched-cas")
	require.NoError(t, err)
	assert.Nil(t, got)
}
