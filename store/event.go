package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const eventColumns = "id, gameserver_id, worker_id, type, actor, data, created_at"

type EventStore struct {
	db *sql.DB
}

func NewEventStore(db *sql.DB) *EventStore {
	return &EventStore{db: db}
}

func scanEvent(scanner interface{ Scan(...any) error }) (*model.Event, error) {
	var e model.Event
	err := scanner.Scan(&e.ID, &e.GameserverID, &e.WorkerID, &e.Type, &e.Actor, &e.Data, &e.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *EventStore) CreateEvent(e *model.Event) error {
	_, err := s.db.Exec(
		"INSERT INTO events (id, gameserver_id, worker_id, type, actor, data, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.ID, e.GameserverID, e.WorkerID, e.Type, e.Actor, e.Data, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating event %s: %w", e.ID, err)
	}
	return nil
}

func (s *EventStore) GetEvent(id string) (*model.Event, error) {
	row := s.db.QueryRow("SELECT "+eventColumns+" FROM events WHERE id = ?", id)
	e, err := scanEvent(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting event %s: %w", id, err)
	}
	return e, nil
}

func (s *EventStore) ListEvents(filter model.EventFilter) ([]model.Event, error) {
	query := "SELECT " + eventColumns + " FROM events WHERE 1=1"
	var args []any

	if filter.GameserverID != nil {
		query += " AND gameserver_id = ?"
		args = append(args, *filter.GameserverID)
	}
	if filter.Type != nil {
		if strings.Contains(*filter.Type, "*") || strings.Contains(*filter.Type, "?") {
			query += " AND type GLOB ?"
		} else {
			query += " AND type = ?"
		}
		args = append(args, *filter.Type)
	}
	if filter.WorkerID != nil {
		query += " AND worker_id = ?"
		args = append(args, *filter.WorkerID)
	}

	query += " ORDER BY created_at DESC"

	if filter.Limit <= 0 {
		filter.Limit = 50
	}
	query = filter.Pagination.ApplyToQuery(query, 200)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning event: %w", err)
		}
		events = append(events, *e)
	}
	return events, rows.Err()
}

// PruneEvents deletes events older than the given number of days.
func (s *EventStore) PruneEvents(retentionDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.Exec("DELETE FROM events WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, fmt.Errorf("pruning events: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}
