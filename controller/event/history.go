package event

import (
	"github.com/warsmite/gamejanitor/model"
)

// ActivityStore is the persistence interface for the history service.
type ActivityStore interface {
	ListActivities(f model.ActivityFilter) ([]model.Activity, error)
}

// EventHistoryService queries persisted activity history from the DB.
type EventHistoryService struct {
	store ActivityStore
}

func NewEventHistoryService(store ActivityStore) *EventHistoryService {
	return &EventHistoryService{store: store}
}

func (s *EventHistoryService) List(filter model.ActivityFilter) ([]model.Activity, error) {
	activities, err := s.store.ListActivities(filter)
	if err != nil {
		return nil, err
	}
	if activities == nil {
		activities = []model.Activity{}
	}
	return activities, nil
}
