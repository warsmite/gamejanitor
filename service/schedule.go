package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/models"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type ScheduleService struct {
	db          *sql.DB
	scheduler   *Scheduler
	broadcaster *EventBus
	log         *slog.Logger
}

func NewScheduleService(db *sql.DB, scheduler *Scheduler, broadcaster *EventBus, log *slog.Logger) *ScheduleService {
	return &ScheduleService{db: db, scheduler: scheduler, broadcaster: broadcaster, log: log}
}

func (s *ScheduleService) ListSchedules(gameserverID string) ([]models.Schedule, error) {
	return models.ListSchedules(s.db, gameserverID)
}

func (s *ScheduleService) GetSchedule(gameserverID, scheduleID string) (*models.Schedule, error) {
	return s.getScheduleForGameserver(gameserverID, scheduleID)
}

// getScheduleForGameserver fetches a schedule and verifies it belongs to the expected gameserver.
func (s *ScheduleService) getScheduleForGameserver(gameserverID, scheduleID string) (*models.Schedule, error) {
	schedule, err := models.GetSchedule(s.db, scheduleID)
	if err != nil {
		return nil, fmt.Errorf("getting schedule %s: %w", scheduleID, err)
	}
	if schedule == nil || schedule.GameserverID != gameserverID {
		return nil, ErrNotFoundf("schedule %s not found", scheduleID)
	}
	return schedule, nil
}

func (s *ScheduleService) CreateSchedule(ctx context.Context, schedule *models.Schedule) error {
	if err := schedule.ValidateCreate(); err != nil {
		return err
	}
	if err := validateCronExpr(schedule.CronExpr); err != nil {
		return err
	}

	schedule.ID = uuid.New().String()

	s.log.Info("creating schedule", "id", schedule.ID, "name", schedule.Name, "type", schedule.Type, "gameserver_id", schedule.GameserverID)

	if err := models.CreateSchedule(s.db, schedule); err != nil {
		return err
	}

	if schedule.Enabled {
		if err := s.scheduler.AddSchedule(*schedule); err != nil {
			if delErr := models.DeleteSchedule(s.db, schedule.ID); delErr != nil {
				s.log.Error("failed to clean up schedule after cron registration failure", "id", schedule.ID, "error", delErr)
			}
			return fmt.Errorf("registering schedule with cron: %w", err)
		}
	}

	s.broadcaster.Publish(ScheduleActionEvent{
		Type:         EventScheduleCreate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func (s *ScheduleService) UpdateSchedule(ctx context.Context, schedule *models.Schedule) error {
	if err := schedule.ValidateCreate(); err != nil {
		return err
	}
	if err := validateCronExpr(schedule.CronExpr); err != nil {
		return err
	}

	s.log.Info("updating schedule", "id", schedule.ID)

	if err := models.UpdateSchedule(s.db, schedule); err != nil {
		return err
	}

	if err := s.scheduler.UpdateSchedule(*schedule); err != nil {
		return fmt.Errorf("updating schedule in cron: %w", err)
	}

	s.broadcaster.Publish(ScheduleActionEvent{
		Type:         EventScheduleUpdate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func (s *ScheduleService) DeleteSchedule(ctx context.Context, gameserverID, scheduleID string) error {
	schedule, err := s.getScheduleForGameserver(gameserverID, scheduleID)
	if err != nil {
		return err
	}

	s.log.Info("deleting schedule", "id", scheduleID)

	s.scheduler.RemoveSchedule(scheduleID)
	if err := models.DeleteSchedule(s.db, scheduleID); err != nil {
		return err
	}

	s.broadcaster.Publish(ScheduleActionEvent{
		Type:         EventScheduleDelete,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func (s *ScheduleService) ToggleSchedule(ctx context.Context, gameserverID, scheduleID string) error {
	schedule, err := s.getScheduleForGameserver(gameserverID, scheduleID)
	if err != nil {
		return err
	}

	schedule.Enabled = !schedule.Enabled

	s.log.Info("toggling schedule", "id", scheduleID, "enabled", schedule.Enabled)

	if err := models.UpdateSchedule(s.db, schedule); err != nil {
		return err
	}

	if err := s.scheduler.UpdateSchedule(*schedule); err != nil {
		return fmt.Errorf("updating schedule in cron after toggle: %w", err)
	}

	s.broadcaster.Publish(ScheduleActionEvent{
		Type:         EventScheduleUpdate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func validateCronExpr(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expr); err != nil {
		return ErrBadRequestf("invalid cron expression %q: %v", expr, err)
	}
	return nil
}
