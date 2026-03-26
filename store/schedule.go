package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const scheduleColumns = "id, gameserver_id, name, type, cron_expr, payload, enabled, one_shot, last_run, next_run, created_at"

type ScheduleStore struct {
	db *sql.DB
}

func NewScheduleStore(db *sql.DB) *ScheduleStore {
	return &ScheduleStore{db: db}
}

func scanSchedule(scan func(dest ...any) error) (model.Schedule, error) {
	var s model.Schedule
	var payloadStr string
	err := scan(&s.ID, &s.GameserverID, &s.Name, &s.Type, &s.CronExpr, &payloadStr, &s.Enabled, &s.OneShot, &s.LastRun, &s.NextRun, &s.CreatedAt)
	if err != nil {
		return s, err
	}
	s.Payload = json.RawMessage(payloadStr)
	return s, nil
}

func (s *ScheduleStore) ListSchedules(gameserverID string) ([]model.Schedule, error) {
	rows, err := s.db.Query("SELECT "+scheduleColumns+" FROM schedules WHERE gameserver_id = ? ORDER BY name", gameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	defer rows.Close()

	var schedules []model.Schedule
	for rows.Next() {
		sched, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning schedule row: %w", err)
		}
		schedules = append(schedules, sched)
	}
	return schedules, rows.Err()
}

func (s *ScheduleStore) GetSchedule(id string) (*model.Schedule, error) {
	row := s.db.QueryRow("SELECT "+scheduleColumns+" FROM schedules WHERE id = ?", id)
	sched, err := scanSchedule(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting schedule %s: %w", id, err)
	}
	return &sched, nil
}

func (s *ScheduleStore) CreateSchedule(sched *model.Schedule) error {
	sched.CreatedAt = time.Now()

	_, err := s.db.Exec(
		"INSERT INTO schedules ("+scheduleColumns+") VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		sched.ID, sched.GameserverID, sched.Name, sched.Type, sched.CronExpr, sched.Payload, sched.Enabled, sched.OneShot, sched.LastRun, sched.NextRun, sched.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating schedule %s: %w", sched.ID, err)
	}
	return nil
}

func (s *ScheduleStore) UpdateSchedule(sched *model.Schedule) error {
	result, err := s.db.Exec(
		"UPDATE schedules SET name = ?, type = ?, cron_expr = ?, payload = ?, enabled = ?, one_shot = ?, last_run = ?, next_run = ? WHERE id = ?",
		sched.Name, sched.Type, sched.CronExpr, sched.Payload, sched.Enabled, sched.OneShot, sched.LastRun, sched.NextRun, sched.ID,
	)
	if err != nil {
		return fmt.Errorf("updating schedule %s: %w", sched.ID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for schedule %s: %w", sched.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule %s not found", sched.ID)
	}
	return nil
}

func (s *ScheduleStore) DeleteSchedulesByGameserver(gameserverID string) error {
	_, err := s.db.Exec("DELETE FROM schedules WHERE gameserver_id = ?", gameserverID)
	if err != nil {
		return fmt.Errorf("deleting schedules for gameserver %s: %w", gameserverID, err)
	}
	return nil
}

func (s *ScheduleStore) DeleteSchedule(id string) error {
	result, err := s.db.Exec("DELETE FROM schedules WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting schedule %s: %w", id, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for schedule %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule %s not found", id)
	}
	return nil
}
