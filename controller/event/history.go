package event

import (
	"github.com/warsmite/gamejanitor/model"
)

// EventHistoryService queries persisted event history from the DB.
type EventHistoryService struct {
	store Store
}

func NewEventHistoryService(store Store) *EventHistoryService {
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
