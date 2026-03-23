package service

import (
	"database/sql"

	"github.com/warsmite/gamejanitor/internal/models"
)

// EventHistoryService queries persisted event history from the DB.
type EventHistoryService struct {
	db *sql.DB
}

func NewEventHistoryService(db *sql.DB) *EventHistoryService {
	return &EventHistoryService{db: db}
}

func (s *EventHistoryService) List(filter models.EventFilter) ([]models.Event, error) {
	events, err := models.ListEvents(s.db, filter)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []models.Event{}
	}
	return events, nil
}
