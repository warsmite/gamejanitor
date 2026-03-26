package service

import (
	"database/sql"

	"github.com/warsmite/gamejanitor/model"
)

// EventHistoryService queries persisted event history from the DB.
type EventHistoryService struct {
	db *sql.DB
}

func NewEventHistoryService(db *sql.DB) *EventHistoryService {
	return &EventHistoryService{db: db}
}

func (s *EventHistoryService) List(filter model.EventFilter) ([]model.Event, error) {
	events, err := model.ListEvents(s.db, filter)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []model.Event{}
	}
	return events, nil
}
