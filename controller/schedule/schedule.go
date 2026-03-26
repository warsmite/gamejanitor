package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

// Store covers the schedule DB operations needed by ScheduleService and Scheduler.
type Store interface {
	ListSchedules(gameserverID string) ([]model.Schedule, error)
	GetSchedule(id string) (*model.Schedule, error)
	CreateSchedule(sched *model.Schedule) error
	UpdateSchedule(sched *model.Schedule) error
	DeleteSchedule(id string) error
	DeleteSchedulesByGameserver(gameserverID string) error
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
}

type ScheduleService struct {
	store       Store
	scheduler   *Scheduler
	broadcaster *controller.EventBus
	log         *slog.Logger
}

func NewScheduleService(store Store, scheduler *Scheduler, broadcaster *controller.EventBus, log *slog.Logger) *ScheduleService {
	return &ScheduleService{store: store, scheduler: scheduler, broadcaster: broadcaster, log: log}
}

func (s *ScheduleService) ListSchedules(gameserverID string) ([]model.Schedule, error) {
	return s.store.ListSchedules(gameserverID)
}

func (s *ScheduleService) GetSchedule(gameserverID, scheduleID string) (*model.Schedule, error) {
	return s.getScheduleForGameserver(gameserverID, scheduleID)
}

// getScheduleForGameserver fetches a schedule and verifies it belongs to the expected gameserver.
func (s *ScheduleService) getScheduleForGameserver(gameserverID, scheduleID string) (*model.Schedule, error) {
	schedule, err := s.store.GetSchedule(scheduleID)
	if err != nil {
		return nil, fmt.Errorf("getting schedule %s: %w", scheduleID, err)
	}
	if schedule == nil || schedule.GameserverID != gameserverID {
		return nil, controller.ErrNotFoundf("schedule %s not found", scheduleID)
	}
	return schedule, nil
}

func (s *ScheduleService) CreateSchedule(ctx context.Context, schedule *model.Schedule) error {
	if err := schedule.ValidateCreate(); err != nil {
		return err
	}
	if err := validateCronExpr(schedule.CronExpr); err != nil {
		return err
	}

	schedule.ID = uuid.New().String()

	s.log.Info("creating schedule", "id", schedule.ID, "name", schedule.Name, "type", schedule.Type, "gameserver_id", schedule.GameserverID)

	if err := s.store.CreateSchedule(schedule); err != nil {
		return err
	}

	if schedule.Enabled {
		if err := s.scheduler.AddSchedule(*schedule); err != nil {
			if delErr := s.store.DeleteSchedule(schedule.ID); delErr != nil {
				s.log.Error("failed to clean up schedule after cron registration failure", "id", schedule.ID, "error", delErr)
			}
			return fmt.Errorf("registering schedule with cron: %w", err)
		}
	}

	s.broadcaster.Publish(controller.ScheduleActionEvent{
		Type:         controller.EventScheduleCreate,
		Timestamp:    time.Now(),
		Actor:        controller.ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func (s *ScheduleService) UpdateSchedule(ctx context.Context, schedule *model.Schedule) error {
	if err := schedule.ValidateCreate(); err != nil {
		return err
	}
	if err := validateCronExpr(schedule.CronExpr); err != nil {
		return err
	}

	s.log.Info("updating schedule", "id", schedule.ID)

	if err := s.store.UpdateSchedule(schedule); err != nil {
		return err
	}

	if err := s.scheduler.UpdateSchedule(*schedule); err != nil {
		return fmt.Errorf("updating schedule in cron: %w", err)
	}

	s.broadcaster.Publish(controller.ScheduleActionEvent{
		Type:         controller.EventScheduleUpdate,
		Timestamp:    time.Now(),
		Actor:        controller.ActorFromContext(ctx),
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
	if err := s.store.DeleteSchedule(scheduleID); err != nil {
		return err
	}

	s.broadcaster.Publish(controller.ScheduleActionEvent{
		Type:         controller.EventScheduleDelete,
		Timestamp:    time.Now(),
		Actor:        controller.ActorFromContext(ctx),
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

	if err := s.store.UpdateSchedule(schedule); err != nil {
		return err
	}

	if err := s.scheduler.UpdateSchedule(*schedule); err != nil {
		return fmt.Errorf("updating schedule in cron after toggle: %w", err)
	}

	s.broadcaster.Publish(controller.ScheduleActionEvent{
		Type:         controller.EventScheduleUpdate,
		Timestamp:    time.Now(),
		Actor:        controller.ActorFromContext(ctx),
		GameserverID: schedule.GameserverID,
		Schedule:     schedule,
	})

	return nil
}

func validateCronExpr(expr string) error {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := parser.Parse(expr); err != nil {
		return controller.ErrBadRequestf("invalid cron expression %q: %v", expr, err)
	}
	return nil
}
