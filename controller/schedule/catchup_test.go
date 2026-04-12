package schedule_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

// TestCatchUp_MissedBackup_ExecutesOnce verifies that a backup schedule whose
// next_run is in the past fires exactly once during catch-up on scheduler start.
func TestCatchUp_MissedBackup_ExecutesOnce(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "CatchUp Backup Host",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Create a backup schedule via the service so it gets an ID and is persisted
	sched := &model.Schedule{
		GameserverID: gs.ID,
		Name:         "hourly-backup",
		Type:         "backup",
		CronExpr:     "0 * * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
	require.NoError(t, err)

	// Manipulate next_run to be in the past (simulating downtime)
	s := store.New(svc.DB)
	pastTime := time.Now().Add(-2 * time.Hour)
	sched.NextRun = &pastTime
	sched.LastRun = nil
	err = s.UpdateSchedule(sched)
	require.NoError(t, err)

	// Subscribe to events before starting the scheduler so we catch the completion
	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	// Start scheduler — catch-up runs in a goroutine
	err = svc.Scheduler.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Scheduler.Stop() })

	// Wait for the backup task to complete (or fail — both prove it executed)
	deadline := time.After(5 * time.Second)
	executed := 0
collect:
	for {
		select {
		case evt := <-ch:
			evtType := evt.EventType()
			if evtType == event.EventScheduleTaskCompleted || evtType == event.EventScheduleTaskFailed {
				if e, ok := evt.(event.Event); ok {
					if data, ok := e.Data.(*event.ScheduledTaskData); ok && data.TaskType == "backup" {
						executed++
					}
				}
			}
		case <-deadline:
			break collect
		}
		// Brief yield after first execution to see if a duplicate fires
		if executed == 1 {
			time.Sleep(200 * time.Millisecond)
			break collect
		}
	}
	assert.Equal(t, 1, executed, "missed backup should execute exactly once during catch-up")
}

// TestCatchUp_MissedRestart_Skipped verifies that restart and command schedules
// are NOT caught up on startup — they're point-in-time actions that don't make
// sense to replay after downtime.
func TestCatchUp_MissedRestart_Skipped(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "CatchUp Restart Host",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	// Create restart and command schedules
	for _, schedType := range []string{"restart", "command"} {
		payload := json.RawMessage(`{}`)
		if schedType == "command" {
			payload = json.RawMessage(`{"command":"say hello"}`)
		}
		sched := &model.Schedule{
			GameserverID: gs.ID,
			Name:         "missed-" + schedType,
			Type:         schedType,
			CronExpr:     "0 * * * *",
			Payload:      payload,
			Enabled:      true,
		}
		err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
		require.NoError(t, err)

		// Set next_run in the past
		s := store.New(svc.DB)
		pastTime := time.Now().Add(-2 * time.Hour)
		sched.NextRun = &pastTime
		sched.LastRun = nil
		err = s.UpdateSchedule(sched)
		require.NoError(t, err)
	}

	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	err = svc.Scheduler.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Scheduler.Stop() })

	// Collect events for a short window — restart/command should emit "missed" not "completed"
	deadline := time.After(2 * time.Second)
	var missedCount int
	var executedCount int
collect:
	for {
		select {
		case evt := <-ch:
			if e, ok := evt.(event.Event); ok {
				if data, ok := e.Data.(*event.ScheduledTaskData); ok {
					switch e.Type {
					case event.EventScheduleTaskMissed:
						missedCount++
					case event.EventScheduleTaskCompleted, event.EventScheduleTaskFailed:
						if data.TaskType == "restart" || data.TaskType == "command" {
							executedCount++
						}
					}
				}
			}
		case <-deadline:
			break collect
		}
	}
	assert.Equal(t, 2, missedCount, "both restart and command should emit missed events")
	assert.Equal(t, 0, executedCount, "restart and command should not be executed during catch-up")
}

// TestCatchUp_NotMissed_NoAction verifies that schedules with a future next_run
// are not triggered by catch-up logic on startup.
func TestCatchUp_NotMissed_NoAction(t *testing.T) {
	t.Parallel()
	svc := testutil.NewTestServices(t)
	testutil.RegisterFakeWorker(t, svc, "worker-1")
	ctx := testutil.TestContext()

	gs := &model.Gameserver{
		Name:   "CatchUp NoAction Host",
		GameID: testutil.TestGameID,
		Env:    model.Env{"REQUIRED_VAR": "v"},
	}
	_, err := svc.Manager.Create(ctx, gs)
	require.NoError(t, err)

	sched := &model.Schedule{
		GameserverID: gs.ID,
		Name:         "future-backup",
		Type:         "backup",
		CronExpr:     "0 * * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	err = svc.ScheduleSvc.CreateSchedule(ctx, sched)
	require.NoError(t, err)

	// next_run should already be in the future from addEntry, but set it explicitly
	s := store.New(svc.DB)
	futureTime := time.Now().Add(2 * time.Hour)
	recentRun := time.Now().Add(-30 * time.Minute)
	sched.NextRun = &futureTime
	sched.LastRun = &recentRun
	err = s.UpdateSchedule(sched)
	require.NoError(t, err)

	ch, unsub := svc.Broadcaster.Subscribe()
	defer unsub()

	err = svc.Scheduler.Start(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { svc.Scheduler.Stop() })

	// Wait briefly — nothing should fire
	deadline := time.After(200 * time.Millisecond)
	for {
		select {
		case evt := <-ch:
			if e, ok := evt.(event.Event); ok {
				if data, ok := e.Data.(*event.ScheduledTaskData); ok {
					if data.TaskType == "backup" {
						t.Fatalf("backup task should not have fired, got event type %s", e.Type)
					}
				}
			}
		case <-deadline:
			return // success — no backup events
		}
	}
}
