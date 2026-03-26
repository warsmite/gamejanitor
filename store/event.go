package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/warsmite/gamejanitor/model"
)

const eventColumns = "id, event_type, gameserver_id, actor, data, created_at"

type EventStore struct {
	db *sql.DB
}

func NewEventStore(db *sql.DB) *EventStore {
	return &EventStore{db: db}
}

func scanEvent(scan func(dest ...any) error) (model.Event, error) {
	var e model.Event
	err := scan(&e.ID, &e.EventType, &e.GameserverID, &e.Actor, &e.Data, &e.CreatedAt)
	return e, err
}

func (s *EventStore) CreateEvent(e *model.Event) error {
	_, err := s.db.Exec(
		`INSERT INTO events (`+eventColumns+`) VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.EventType, e.GameserverID, e.Actor, e.Data, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating event: %w", err)
	}
	return nil
}

func (s *EventStore) ListEvents(f model.EventFilter) ([]model.Event, error) {
	query := `SELECT ` + eventColumns + ` FROM events WHERE 1=1`
	args := []any{}

	if f.EventType != "" {
		query += ` AND event_type GLOB ?`
		args = append(args, f.EventType)
	}
	if f.GameserverID != "" {
		query += ` AND gameserver_id = ?`
		args = append(args, f.GameserverID)
	}
	if len(f.AllowedGameserverIDs) > 0 {
		placeholders := strings.Repeat("?,", len(f.AllowedGameserverIDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += ` AND gameserver_id IN (` + placeholders + `)`
		for _, id := range f.AllowedGameserverIDs {
			args = append(args, id)
		}
	}

	query += ` ORDER BY created_at DESC`

	// Events default to 50 if no limit specified
	if f.Limit <= 0 {
		f.Limit = 50
	}
	query = f.Pagination.ApplyToQuery(query, 200)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing events: %w", err)
	}
	defer rows.Close()

	var events []model.Event
	for rows.Next() {
		e, err := scanEvent(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning event row: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *EventStore) PruneEvents(retentionDays int) (int64, error) {
	result, err := s.db.Exec(
		`DELETE FROM events WHERE created_at < datetime('now', '-' || ? || ' days')`,
		retentionDays,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning events: %w", err)
	}
	return result.RowsAffected()
}
