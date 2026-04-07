package cluster

import "github.com/warsmite/gamejanitor/model"

// Store covers all DB access needed by the cluster domain.
type Store interface {
	GetGameserver(id string) (*model.Gameserver, error)
	UpdateGameserver(gs *model.Gameserver) error
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
	CreateEvent(e *model.Event) error
	GetWorkerNode(id string) (*model.WorkerNode, error)
	AllocatedMemoryByNode(nodeID string) (int, error)
	AllocatedCPUByNode(nodeID string) (float64, error)
	AllocatedStorageByNode(nodeID string) (int, error)
	AllocatedMemoryByNodeExcluding(nodeID, excludeID string) (int, error)
	AllocatedCPUByNodeExcluding(nodeID, excludeID string) (float64, error)
	AllocatedStorageByNodeExcluding(nodeID, excludeID string) (int, error)
}
