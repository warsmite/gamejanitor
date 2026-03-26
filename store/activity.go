package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const activityColumns = "id, gameserver_id, worker_id, type, status, actor, data, error, started_at, completed_at"

type ActivityStore struct {
	db *sql.DB
}

func NewActivityStore(db *sql.DB) *ActivityStore {
	return &ActivityStore{db: db}
}

func scanActivity(scanner interface{ Scan(...any) error }) (*model.Activity, error) {
	var a model.Activity
	err := scanner.Scan(&a.ID, &a.GameserverID, &a.WorkerID, &a.Type, &a.Status, &a.Actor, &a.Data, &a.Error, &a.StartedAt, &a.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *ActivityStore) CreateActivity(a *model.Activity) error {
	_, err := s.db.Exec(
		"INSERT INTO activity (id, gameserver_id, worker_id, type, status, actor, data, error, started_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		a.ID, a.GameserverID, a.WorkerID, a.Type, a.Status, a.Actor, a.Data, a.Error, a.StartedAt, a.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("creating activity %s: %w", a.ID, err)
	}
	return nil
}

func (s *ActivityStore) GetActivity(id string) (*model.Activity, error) {
	row := s.db.QueryRow("SELECT "+activityColumns+" FROM activity WHERE id = ?", id)
	a, err := scanActivity(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting activity %s: %w", id, err)
	}
	return a, nil
}

func (s *ActivityStore) CompleteActivity(id string) error {
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE activity SET status = ?, completed_at = ? WHERE id = ?",
		model.ActivityCompleted, now, id,
	)
	if err != nil {
		return fmt.Errorf("completing activity %s: %w", id, err)
	}
	return nil
}

func (s *ActivityStore) FailActivity(id string, errMsg string) error {
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE activity SET status = ?, error = ?, completed_at = ? WHERE id = ?",
		model.ActivityFailed, errMsg, now, id,
	)
	if err != nil {
		return fmt.Errorf("failing activity %s: %w", id, err)
	}
	return nil
}

// AbandonRunningActivities marks all running activities as abandoned.
// Called on controller startup to clean up activities from a previous crash.
func (s *ActivityStore) AbandonRunningActivities() (int, error) {
	now := time.Now()
	result, err := s.db.Exec(
		"UPDATE activity SET status = ?, error = 'controller restarted', completed_at = ? WHERE status = ?",
		model.ActivityAbandoned, now, model.ActivityRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("abandoning running activities: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// HasRunningActivity returns true if the gameserver has a running activity.
// Used as a mutex to prevent concurrent mutations.
func (s *ActivityStore) HasRunningActivity(gameserverID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM activity WHERE gameserver_id = ? AND status = ?",
		gameserverID, model.ActivityRunning,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking running activities for %s: %w", gameserverID, err)
	}
	return count > 0, nil
}

func (s *ActivityStore) ListActivities(filter model.ActivityFilter) ([]model.Activity, error) {
	query := "SELECT " + activityColumns + " FROM activity WHERE 1=1"
	var args []any

	if filter.GameserverID != nil {
		query += " AND gameserver_id = ?"
		args = append(args, *filter.GameserverID)
	}
	if filter.Type != nil {
		// Support GLOB patterns for type filtering (e.g. "gameserver.*")
		if strings.Contains(*filter.Type, "*") || strings.Contains(*filter.Type, "?") {
			query += " AND type GLOB ?"
		} else {
			query += " AND type = ?"
		}
		args = append(args, *filter.Type)
	}
	if filter.Status != nil {
		query += " AND status = ?"
		args = append(args, *filter.Status)
	}
	if filter.WorkerID != nil {
		query += " AND worker_id = ?"
		args = append(args, *filter.WorkerID)
	}

	query += " ORDER BY started_at DESC"

	// Default to 50 if no limit specified
	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	query = filter.Pagination.ApplyToQuery(query, 200)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing activities: %w", err)
	}
	defer rows.Close()

	var activities []model.Activity
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning activity: %w", err)
		}
		activities = append(activities, *a)
	}
	return activities, rows.Err()
}

// PruneActivities deletes completed/failed/abandoned activities older than the given number of days.
func (s *ActivityStore) PruneActivities(retentionDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.Exec(
		"DELETE FROM activity WHERE status != ? AND completed_at < ?",
		model.ActivityRunning, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning activities: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}
