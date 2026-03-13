package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/robfig/cron/v3"
)

type Scheduler struct {
	cron          *cron.Cron
	db            *sql.DB
	backupSvc     *BackupService
	gameserverSvc *GameserverService
	consoleSvc    *ConsoleService
	log           *slog.Logger
	entries       map[string]cron.EntryID
	mu            sync.Mutex
}

func NewScheduler(db *sql.DB, backupSvc *BackupService, gameserverSvc *GameserverService, consoleSvc *ConsoleService, log *slog.Logger) *Scheduler {
	return &Scheduler{
		cron:          cron.New(),
		db:            db,
		backupSvc:     backupSvc,
		gameserverSvc: gameserverSvc,
		consoleSvc:    consoleSvc,
		log:           log,
		entries:       make(map[string]cron.EntryID),
	}
}

func (s *Scheduler) Start(ctx context.Context) error {
	s.log.Info("loading schedules into cron")

	// Load all gameservers to iterate their schedules
	gameservers, err := models.ListGameservers(s.db, models.GameserverFilter{})
	if err != nil {
		return fmt.Errorf("listing gameservers for scheduler: %w", err)
	}

	for _, gs := range gameservers {
		schedules, err := models.ListSchedules(s.db, gs.ID)
		if err != nil {
			s.log.Error("listing schedules for gameserver", "gameserver_id", gs.ID, "error", err)
			continue
		}
		for _, sched := range schedules {
			if !sched.Enabled {
				continue
			}
			if err := s.addEntry(sched); err != nil {
				s.log.Error("failed to register schedule", "schedule_id", sched.ID, "error", err)
			}
		}
	}

	s.cron.Start()
	s.log.Info("scheduler started", "entries", len(s.entries))
	return nil
}

func (s *Scheduler) Stop() {
	s.log.Info("stopping scheduler")
	s.cron.Stop()
}

func (s *Scheduler) AddSchedule(schedule models.Schedule) error {
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

	if entryID, ok := s.entries[scheduleID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, scheduleID)
		s.log.Debug("removed schedule from cron", "schedule_id", scheduleID)
	}
}

func (s *Scheduler) UpdateSchedule(schedule models.Schedule) error {
	s.RemoveSchedule(schedule.ID)
	if schedule.Enabled {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.addEntry(schedule)
	}
	return nil
}

// addEntry registers a schedule with cron. Must be called with s.mu held.
func (s *Scheduler) addEntry(schedule models.Schedule) error {
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
		if err := models.UpdateSchedule(s.db, &schedule); err != nil {
			s.log.Warn("failed to update next_run for schedule", "schedule_id", schedID, "error", err)
		}
	}

	s.log.Debug("registered schedule with cron", "schedule_id", schedID, "cron_expr", schedule.CronExpr)
	return nil
}

func (s *Scheduler) executeTask(scheduleID string) {
	schedule, err := models.GetSchedule(s.db, scheduleID)
	if err != nil || schedule == nil {
		s.log.Error("failed to load schedule for execution", "schedule_id", scheduleID, "error", err)
		return
	}

	ctx := context.Background()
	s.log.Info("executing scheduled task", "schedule_id", scheduleID, "type", schedule.Type, "gameserver_id", schedule.GameserverID)

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
			s.log.Error("failed to parse command payload", "schedule_id", scheduleID, "error", err)
			return
		}
		_, taskErr = s.consoleSvc.SendCommand(ctx, schedule.GameserverID, payload.Command)
	case "update":
		taskErr = s.gameserverSvc.UpdateServerGame(ctx, schedule.GameserverID)
	default:
		s.log.Error("unknown schedule type", "schedule_id", scheduleID, "type", schedule.Type)
		return
	}

	if taskErr != nil {
		s.log.Error("scheduled task failed", "schedule_id", scheduleID, "type", schedule.Type, "error", taskErr)
	} else {
		s.log.Info("scheduled task completed", "schedule_id", scheduleID, "type", schedule.Type)
	}

	// Update last_run and next_run
	now := time.Now()
	schedule.LastRun = &now

	s.mu.Lock()
	if entryID, ok := s.entries[scheduleID]; ok {
		entry := s.cron.Entry(entryID)
		if !entry.Next.IsZero() {
			nextRun := entry.Next
			schedule.NextRun = &nextRun
		}
	}
	s.mu.Unlock()

	if err := models.UpdateSchedule(s.db, schedule); err != nil {
		s.log.Error("failed to update schedule after execution", "schedule_id", scheduleID, "error", err)
	}
}

// RemoveSchedulesByGameserver removes all cron entries for a gameserver.
func (s *Scheduler) RemoveSchedulesByGameserver(gameserverID string) {
	schedules, err := models.ListSchedules(s.db, gameserverID)
	if err != nil {
		s.log.Error("listing schedules for removal", "gameserver_id", gameserverID, "error", err)
		return
	}
	for _, sched := range schedules {
		s.RemoveSchedule(sched.ID)
	}
}
