package schedule

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
	"github.com/robfig/cron/v3"
)

// GameserverOps is the subset of gameserver operations the scheduler needs.
type GameserverOps interface {
	Restart(ctx context.Context, id string) error
	UpdateServerGame(ctx context.Context, id string) error
}

// BackupOps is the subset of backup operations the scheduler needs.
type BackupOps interface {
	CreateBackup(ctx context.Context, gameserverID string, name string) (*model.Backup, error)
}

// ConsoleOps is the subset of console operations the scheduler needs.
type ConsoleOps interface {
	SendCommand(ctx context.Context, gameserverID string, command string) (string, error)
}

type Scheduler struct {
	cron          *cron.Cron
	store         Store
	backupSvc     BackupOps
	gameserverSvc GameserverOps
	consoleSvc    ConsoleOps
	broadcaster   *controller.EventBus
	log           *slog.Logger
	entries       map[string]cron.EntryID
	mu            sync.Mutex
}

func NewScheduler(store Store, backupSvc BackupOps, gameserverSvc GameserverOps, consoleSvc ConsoleOps, broadcaster *controller.EventBus, log *slog.Logger) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		store:         store,
		backupSvc:     backupSvc,
		gameserverSvc: gameserverSvc,
		consoleSvc:    consoleSvc,
		broadcaster:   broadcaster,
		log:           log,
		entries:       make(map[string]cron.EntryID),
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.log.Info("loading schedules into cron")

	// Load all gameservers to iterate their schedules
	gameservers, err := s.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		return fmt.Errorf("listing gameservers for scheduler: %w", err)
	}

	for _, gs := range gameservers {
		schedules, err := s.store.ListSchedules(gs.ID)
		if err != nil {
			s.log.Error("listing schedules for gameserver", "gameserver", gs.ID, "error", err)
			continue
		}
		for _, sched := range schedules {
			if !sched.Enabled {
				continue
			}
			if err := s.addEntry(sched); err != nil {
				s.log.Error("failed to register schedule", "schedule", sched.ID, "error", err)
			}
		}
	}

	s.cron.Start()
	s.log.Info("scheduler started", "entries", len(s.entries))

	// Check for missed schedules during downtime and catch up where appropriate
	go s.catchUpMissed()

	return nil
}

// shouldCatchUp returns true for schedule types that should run immediately
// when missed (data protection, keeping game current). Disruptive or
// time-sensitive types (restart, command) are skipped with an event log.
func shouldCatchUp(schedType string) bool {
	return schedType == "backup" || schedType == "update"
}

func (s *Scheduler) catchUpMissed() {
	now := time.Now()

	gameservers, err := s.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		s.log.Error("failed to list gameservers for missed schedule check", "error", err)
		return
	}

	for _, gs := range gameservers {
		schedules, err := s.store.ListSchedules(gs.ID)
		if err != nil {
			continue
		}
		for _, sched := range schedules {
			if !sched.Enabled || sched.NextRun == nil {
				continue
			}
			if sched.NextRun.After(now) {
				continue
			}
			// next_run is in the past — this schedule was missed
			if sched.LastRun != nil && sched.LastRun.After(*sched.NextRun) {
				continue // already ran after the missed time (shouldn't happen, but guard)
			}

			if shouldCatchUp(sched.Type) {
				s.log.Warn("catching up missed schedule",
					"schedule", sched.ID, "type", sched.Type,
					"gameserver", sched.GameserverID, "was_due", sched.NextRun)
				s.executeTask(sched.ID)
			} else {
				s.log.Warn("skipping missed schedule (not catch-up eligible)",
					"schedule", sched.ID, "type", sched.Type,
					"gameserver", sched.GameserverID, "was_due", sched.NextRun)
				s.broadcaster.Publish(controller.NewEvent(controller.EventScheduleTaskMissed, sched.GameserverID, controller.Actor{Type: "schedule", ScheduleID: sched.ID}, &controller.ScheduledTaskData{
					Schedule: &sched,
					TaskType: sched.Type,
				}))
			}
		}
	}
}

func (s *Scheduler) Stop() {
	s.log.Info("stopping scheduler")
	s.cron.Stop()
}

func (s *Scheduler) AddSchedule(schedule model.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !schedule.Enabled {
		return nil
	}
	return s.addEntry(schedule)
}

func (s *Scheduler) RemoveSchedule(scheduleID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeEntryLocked(scheduleID)
}

func (s *Scheduler) UpdateSchedule(schedule model.Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.removeEntryLocked(schedule.ID)
	if schedule.Enabled {
		return s.addEntry(schedule)
	}
	return nil
}

// removeEntryLocked removes a cron entry. Must be called with s.mu held.
func (s *Scheduler) removeEntryLocked(scheduleID string) {
	if entryID, ok := s.entries[scheduleID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, scheduleID)
		s.log.Debug("removed schedule from cron", "schedule", scheduleID)
	}
}

// addEntry registers a schedule with cron. Must be called with s.mu held.
func (s *Scheduler) addEntry(schedule model.Schedule) error {
	schedID := schedule.ID
	entryID, err := s.cron.AddFunc(schedule.CronExpr, func() {
		s.executeTask(schedID)
	})
	if err != nil {
		return fmt.Errorf("adding cron entry for schedule %s: %w", schedID, err)
	}

	s.entries[schedID] = entryID

	// Compute and store next_run
	entry := s.cron.Entry(entryID)
	if !entry.Next.IsZero() {
		nextRun := entry.Next
		schedule.NextRun = &nextRun
		if err := s.store.UpdateSchedule(&schedule); err != nil {
			s.log.Warn("failed to update next_run for schedule", "schedule", schedID, "error", err)
		}
	}

	s.log.Debug("registered schedule with cron", "schedule", schedID, "cron_expr", schedule.CronExpr)
	return nil
}

func (s *Scheduler) executeTask(scheduleID string) {
	schedule, err := s.store.GetSchedule(scheduleID)
	if err != nil || schedule == nil {
		s.log.Error("failed to load schedule for execution", "schedule", scheduleID, "error", err)
		return
	}

	// No operation timeout. Scheduled tasks include game updates (50GB+ image pulls)
	// and backups (500GB+ volumes) that can legitimately run for hours on slow
	// networks. The cron library won't fire the next run until this one completes.
	ctx := controller.SetActorInContext(context.Background(), controller.Actor{Type: "schedule", ScheduleID: scheduleID})
	s.log.Info("executing scheduled task", "schedule", scheduleID, "type", schedule.Type, "gameserver", schedule.GameserverID)

	var taskErr error
	switch schedule.Type {
	case "restart":
		taskErr = s.gameserverSvc.Restart(ctx, schedule.GameserverID)
	case "backup":
		_, taskErr = s.backupSvc.CreateBackup(ctx, schedule.GameserverID, "Scheduled backup")
	case "command":
		var payload struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(schedule.Payload, &payload); err != nil {
			s.log.Error("failed to parse command payload", "schedule", scheduleID, "error", err)
			return
		}
		_, taskErr = s.consoleSvc.SendCommand(ctx, schedule.GameserverID, payload.Command)
	case "update":
		taskErr = s.gameserverSvc.UpdateServerGame(ctx, schedule.GameserverID)
	default:
		s.log.Error("unknown schedule type", "schedule", scheduleID, "type", schedule.Type)
		return
	}

	if taskErr != nil {
		s.log.Error("scheduled task failed", "schedule", scheduleID, "type", schedule.Type, "error", taskErr)
		s.broadcaster.Publish(controller.NewEvent(controller.EventScheduleTaskFailed, schedule.GameserverID, controller.Actor{Type: "schedule", ScheduleID: scheduleID}, &controller.ScheduledTaskData{
			Schedule: schedule,
			TaskType: schedule.Type,
			Error:    taskErr.Error(),
		}))
	} else {
		s.log.Info("scheduled task completed", "schedule", scheduleID, "type", schedule.Type)
		s.broadcaster.Publish(controller.NewEvent(controller.EventScheduleTaskCompleted, schedule.GameserverID, controller.Actor{Type: "schedule", ScheduleID: scheduleID}, &controller.ScheduledTaskData{
			Schedule: schedule,
			TaskType: schedule.Type,
		}))
	}

	// Update last_run and next_run
	now := time.Now()
	schedule.LastRun = &now

	if schedule.OneShot {
		// One-shot schedules disable after first execution
		schedule.Enabled = false
		schedule.NextRun = nil
		s.RemoveSchedule(scheduleID)
		s.log.Info("one-shot schedule completed, disabling", "schedule", scheduleID)
	} else {
		s.mu.Lock()
		if entryID, ok := s.entries[scheduleID]; ok {
			entry := s.cron.Entry(entryID)
			if !entry.Next.IsZero() {
				nextRun := entry.Next
				schedule.NextRun = &nextRun
			}
		}
		s.mu.Unlock()
	}

	if err := s.store.UpdateSchedule(schedule); err != nil {
		s.log.Error("failed to update schedule after execution", "schedule", scheduleID, "error", err)
	}
}

// RemoveSchedulesByGameserver removes all cron entries for a gameserver.
func (s *Scheduler) RemoveSchedulesByGameserver(gameserverID string) {
	schedules, err := s.store.ListSchedules(gameserverID)
	if err != nil {
		s.log.Error("listing schedules for removal", "gameserver", gameserverID, "error", err)
		return
	}
	for _, sched := range schedules {
		s.RemoveSchedule(sched.ID)
	}
}
