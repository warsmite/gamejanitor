package event

import (
	"github.com/warsmite/gamejanitor/model"
)

// EventStore is the persistence interface for the history service.
type EventStore interface {
	ListEvents(f model.EventFilter) ([]model.Event, error)
}

// EventHistoryService queries persisted event history from the DB.
type EventHistoryService struct {
	store EventStore
}

func NewEventHistoryService(store EventStore) *EventHistoryService {
	return &EventHistoryService{store: store}
}

func (s *EventHistoryService) List(filter model.EventFilter) ([]model.Event, error) {
	events, err := s.store.ListEvents(filter)
	if err != nil {
		return nil, err
	}
	if events == nil {
		events = []model.Event{}
	}
	return events, nil
}
