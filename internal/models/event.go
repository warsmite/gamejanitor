package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Event struct {
	ID           string          `json:"id"`
	EventType    string          `json:"event_type"`
	GameserverID string          `json:"gameserver_id,omitempty"`
	Actor        json.RawMessage `json:"actor"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
}

func CreateEvent(db *sql.DB, e *Event) error {
	_, err := db.Exec(
		`INSERT INTO events (id, event_type, gameserver_id, actor, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.EventType, e.GameserverID, e.Actor, e.Data, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating event: %w", err)
	}
	return nil
}

type EventFilter struct {
	EventType    string // glob pattern
	GameserverID string
	Pagination
}

func ListEvents(db *sql.DB, f EventFilter) ([]Event, error) {
	query := `SELECT id, event_type, gameserver_id, actor, data, created_at FROM events WHERE 1=1`
	args := []any{}

	if f.EventType != "" {
		query += ` AND event_type GLOB ?`
		args = append(args, f.EventType)
	}
	if f.GameserverID != "" {
		query += ` AND gameserver_id = ?`
		args = append(args, f.GameserverID)
	}

	query += ` ORDER BY created_at DESC`

	// Events default to 50 if no limit specified
	if f.Limit <= 0 {
		f.Limit = 50
	}
	query = f.Pagination.ApplyToQuery(query, 200)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.EventType, &e.GameserverID, &e.Actor, &e.Data, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning event row: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func PruneEvents(db *sql.DB, retentionDays int) (int64, error) {
	result, err := db.Exec(
		`DELETE FROM events WHERE created_at < datetime('now', '-' || ? || ' days')`,
		retentionDays,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning events: %w", err)
	}
	return result.RowsAffected()
}
