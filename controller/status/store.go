package status

import "github.com/warsmite/gamejanitor/model"

// Store covers all DB access needed by the status domain.
type Store interface {
	GetGameserver(id string) (*model.Gameserver, error)
	UpdateGameserver(gs *model.Gameserver) error
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
}
