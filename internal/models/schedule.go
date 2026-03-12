package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Schedule struct {
	ID           string          `json:"id"`
	GameserverID string          `json:"gameserver_id"`
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	CronExpr     string          `json:"cron_expr"`
	Payload      json.RawMessage `json:"payload"`
	Enabled      bool            `json:"enabled"`
	LastRun      *time.Time      `json:"last_run"`
	NextRun      *time.Time      `json:"next_run"`
	CreatedAt    time.Time       `json:"created_at"`
}

func ListSchedules(db *sql.DB, gameserverID string) ([]Schedule, error) {
	rows, err := db.Query("SELECT id, gameserver_id, name, type, cron_expr, payload, enabled, last_run, next_run, created_at FROM schedules WHERE gameserver_id = ? ORDER BY name", gameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing schedules: %w", err)
	}
	defer rows.Close()

	var schedules []Schedule
	for rows.Next() {
		s, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning schedule row: %w", err)
		}
		schedules = append(schedules, s)
	}
	return schedules, rows.Err()
}

func GetSchedule(db *sql.DB, id string) (*Schedule, error) {
	row := db.QueryRow("SELECT id, gameserver_id, name, type, cron_expr, payload, enabled, last_run, next_run, created_at FROM schedules WHERE id = ?", id)
	s, err := scanSchedule(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting schedule %s: %w", id, err)
	}
	return &s, nil
}

func scanSchedule(scan func(dest ...any) error) (Schedule, error) {
	var s Schedule
	var payloadStr string
	err := scan(&s.ID, &s.GameserverID, &s.Name, &s.Type, &s.CronExpr, &payloadStr, &s.Enabled, &s.LastRun, &s.NextRun, &s.CreatedAt)
	if err != nil {
		return s, err
	}
	s.Payload = json.RawMessage(payloadStr)
	return s, nil
}

func CreateSchedule(db *sql.DB, s *Schedule) error {
	s.CreatedAt = time.Now()

	_, err := db.Exec(
		"INSERT INTO schedules (id, gameserver_id, name, type, cron_expr, payload, enabled, last_run, next_run, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		s.ID, s.GameserverID, s.Name, s.Type, s.CronExpr, s.Payload, s.Enabled, s.LastRun, s.NextRun, s.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating schedule %s: %w", s.ID, err)
	}
	return nil
}

func UpdateSchedule(db *sql.DB, s *Schedule) error {
	result, err := db.Exec(
		"UPDATE schedules SET name = ?, type = ?, cron_expr = ?, payload = ?, enabled = ?, last_run = ?, next_run = ? WHERE id = ?",
		s.Name, s.Type, s.CronExpr, s.Payload, s.Enabled, s.LastRun, s.NextRun, s.ID,
	)
	if err != nil {
		return fmt.Errorf("updating schedule %s: %w", s.ID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for schedule %s: %w", s.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("schedule %s not found", s.ID)
	}
	return nil
}

func DeleteSchedule(db *sql.DB, id string) error {
	result, err := db.Exec("DELETE FROM schedules WHERE id = ?", id)
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
