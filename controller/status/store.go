package status

import "github.com/warsmite/gamejanitor/model"

// Store covers all DB access needed by the status domain.
type Store interface {
	GetGameserver(id string) (*model.Gameserver, error)
	UpdateGameserver(gs *model.Gameserver) error
	TransitionStatus(id string, fromStatuses []string, toStatus string, errorReason string) (bool, error)
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
	CreateEvent(e *model.Event) error
}
