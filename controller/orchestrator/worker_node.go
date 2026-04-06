package orchestrator

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/pkg/validate"
)

// WorkerNodeStore is the persistence interface for worker node operations.
type WorkerNodeStore interface {
	GetWorkerNode(id string) (*model.WorkerNode, error)
	ListWorkerNodes() ([]model.WorkerNode, error)
	SetWorkerNodeName(id string, name string) error
	SetWorkerNodeLimits(id string, maxMemoryMB *int, maxCPU *float64, maxStorageMB *int) error
	SetWorkerNodeCordoned(id string, cordoned bool) error
	SetWorkerNodeTags(id string, tags model.Labels) error
	SetWorkerNodePortRange(id string, start *int, end *int) error
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
}

type WorkerNodeService struct {
	store       WorkerNodeStore
	registry    *Registry
	broadcaster *controller.EventBus
	log         *slog.Logger
}

func NewWorkerNodeService(store WorkerNodeStore, registry *Registry, broadcaster *controller.EventBus, log *slog.Logger) *WorkerNodeService {
	return &WorkerNodeService{store: store, registry: registry, broadcaster: broadcaster, log: log}
}

// WorkerView is the enriched API representation of a worker node.
type WorkerView struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	LanIP             string   `json:"lan_ip"`
	ExternalIP        string   `json:"external_ip"`
	CPUCores          int64    `json:"cpu_cores"`
	MemoryTotalMB     int64    `json:"memory_total_mb"`
	MemoryAvailableMB int64    `json:"memory_available_mb"`
	GameserverCount   int      `json:"gameserver_count"`
	AllocatedMemoryMB int      `json:"allocated_memory_mb"`
	AllocatedCPU      float64  `json:"allocated_cpu"`
	AllocatedStorageMB int     `json:"allocated_storage_mb"`
	DiskTotalMB       int64    `json:"disk_total_mb"`
	DiskAvailableMB   int64    `json:"disk_available_mb"`
	MaxMemoryMB       *int     `json:"max_memory_mb"`
	MaxCPU            *float64 `json:"max_cpu"`
	MaxStorageMB      *int     `json:"max_storage_mb"`
	Cordoned           bool     `json:"cordoned"`
	Tags               model.Labels `json:"tags"`
	PortRangeStart     *int     `json:"port_range_start"`
	PortRangeEnd       *int     `json:"port_range_end"`
	Status             string   `json:"status"`
	LastSeen           *string  `json:"last_seen"`
}

func (s *WorkerNodeService) List() ([]WorkerView, error) {
	if s.registry == nil {
		return []WorkerView{}, nil
	}

	// Show all workers (online and offline) from the registry
	infos := s.registry.ListWorkers()
	gsCount, allocMem, allocCPU, allocStorage := s.nodeStats()

	views := make([]WorkerView, 0, len(infos))
	for _, info := range infos {
		node, _ := s.store.GetWorkerNode(info.ID)
		views = append(views, s.buildView(info, gsCount[info.ID], allocMem[info.ID], allocCPU[info.ID], allocStorage[info.ID], node))
	}
	return views, nil
}

func (s *WorkerNodeService) Get(id string) (*WorkerView, error) {
	if s.registry == nil {
		return nil, controller.ErrNotFound("multi-node not enabled")
	}

	info, ok := s.registry.GetInfo(id)
	if !ok {
		return nil, controller.ErrNotFoundf("worker %s not found", id)
	}

	gsCount, allocMem, allocCPU, allocStorage := s.nodeStats()
	node, _ := s.store.GetWorkerNode(id)
	v := s.buildView(info, gsCount[id], allocMem[id], allocCPU[id], allocStorage[id], node)
	return &v, nil
}

// WorkerNodeUpdate represents a partial update to a worker node.
// Nil pointer fields are not updated. To clear a limit, set it to a pointer to 0.
type WorkerNodeUpdate struct {
	Name           *string       `json:"name,omitempty"`
	MaxMemoryMB    *int          `json:"max_memory_mb,omitempty"`
	MaxCPU         *float64      `json:"max_cpu,omitempty"`
	MaxStorageMB   *int          `json:"max_storage_mb,omitempty"`
	Cordoned       *bool         `json:"cordoned,omitempty"`
	Tags           *model.Labels `json:"tags,omitempty"`
	PortRangeStart *int          `json:"port_range_start,omitempty"`
	PortRangeEnd   *int          `json:"port_range_end,omitempty"`
}

func (u *WorkerNodeUpdate) Validate() error {
	var fe validate.FieldErrors
	fe.MinIntPtr("max_memory_mb", u.MaxMemoryMB, 0)
	fe.MinFloatPtr("max_cpu", u.MaxCPU, 0)
	fe.MinIntPtr("max_storage_mb", u.MaxStorageMB, 0)
	fe.MinIntPtr("port_range_start", u.PortRangeStart, 0)
	fe.MinIntPtr("port_range_end", u.PortRangeEnd, 0)
	if u.PortRangeStart != nil && u.PortRangeEnd != nil && *u.PortRangeStart > *u.PortRangeEnd {
		fe.Add("port_range_end", "must be >= port_range_start")
	}
	return fe.Err()
}

func (s *WorkerNodeService) Update(ctx context.Context, id string, update *WorkerNodeUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}

	if update.Name != nil {
		if err := s.store.SetWorkerNodeName(id, *update.Name); err != nil {
			return err
		}
	}
	if update.MaxMemoryMB != nil || update.MaxCPU != nil || update.MaxStorageMB != nil {
		if err := s.store.SetWorkerNodeLimits(id, update.MaxMemoryMB, update.MaxCPU, update.MaxStorageMB); err != nil {
			return err
		}
	}
	if update.Cordoned != nil {
		if err := s.store.SetWorkerNodeCordoned(id, *update.Cordoned); err != nil {
			return err
		}
	}
	if update.Tags != nil {
		if err := s.store.SetWorkerNodeTags(id, *update.Tags); err != nil {
			return err
		}
	}
	if update.PortRangeStart != nil || update.PortRangeEnd != nil {
		// Merge with existing values so you can update one without the other
		existing, err := s.store.GetWorkerNode(id)
		if err != nil {
			return err
		}
		if existing == nil {
			return controller.ErrNotFoundf("worker %s not found", id)
		}
		start := existing.PortRangeStart
		end := existing.PortRangeEnd
		if update.PortRangeStart != nil {
			start = update.PortRangeStart
		}
		if update.PortRangeEnd != nil {
			end = update.PortRangeEnd
		}

		// Validate no overlap with other workers' ranges
		if start != nil && end != nil {
			if err := s.validatePortRangeNoOverlap(id, *start, *end); err != nil {
				return err
			}
		}

		if err := s.store.SetWorkerNodePortRange(id, start, end); err != nil {
			return err
		}
	}

	view, _ := s.Get(id)
	s.broadcaster.Publish(controller.NewEvent(controller.EventWorkerUpdated, "", controller.ActorFromContext(ctx), &controller.WorkerActionData{
		WorkerID: id,
		Worker:   view,
	}))

	return nil
}

func (s *WorkerNodeService) buildView(info WorkerInfo, gsCount, allocMem int, allocCPU float64, allocStorage int, node *model.WorkerNode) WorkerView {
	var lastSeen *string
	if !info.LastSeen.IsZero() {
		ls := info.LastSeen.UTC().Format(time.RFC3339)
		lastSeen = &ls
	}

	v := WorkerView{
		ID:                info.ID,
		LanIP:             info.LanIP,
		ExternalIP:        info.ExternalIP,
		CPUCores:          info.CPUCores,
		MemoryTotalMB:     info.MemoryTotalMB,
		MemoryAvailableMB: info.MemoryAvailableMB,
		GameserverCount:   gsCount,
		AllocatedMemoryMB: allocMem,
		AllocatedCPU:       allocCPU,
		AllocatedStorageMB: allocStorage,
		DiskTotalMB:       info.DiskTotalMB,
		DiskAvailableMB:   info.DiskAvailableMB,
		Status:            info.Status,
		LastSeen:          lastSeen,
	}
	if node != nil {
		v.Name = node.Name
		v.MaxMemoryMB = node.MaxMemoryMB
		v.MaxCPU = node.MaxCPU
		v.MaxStorageMB = node.MaxStorageMB
		v.Cordoned = node.Cordoned
		v.Tags = node.Tags
		v.PortRangeStart = node.PortRangeStart
		v.PortRangeEnd = node.PortRangeEnd
	}
	if v.Tags == nil {
		v.Tags = model.Labels{}
	}
	return v
}

// validatePortRangeNoOverlap checks that a worker's port range doesn't overlap
// with any other worker's range. Only called when port_uniqueness is "cluster"
// or when ranges are set (overlapping ranges with node-scoped uniqueness are
// harmless but confusing, so we reject them everywhere).
func (s *WorkerNodeService) validatePortRangeNoOverlap(workerID string, start, end int) error {
	nodes, err := s.store.ListWorkerNodes()
	if err != nil {
		return fmt.Errorf("listing workers for port range validation: %w", err)
	}
	for _, n := range nodes {
		if n.ID == workerID || n.PortRangeStart == nil || n.PortRangeEnd == nil {
			continue
		}
		// Ranges overlap if one starts before the other ends
		if start <= *n.PortRangeEnd && end >= *n.PortRangeStart {
			return controller.ErrBadRequestf("port range %d-%d overlaps with worker %s (%d-%d)",
				start, end, n.ID, *n.PortRangeStart, *n.PortRangeEnd)
		}
	}
	return nil
}

func (s *WorkerNodeService) nodeStats() (gsCount map[string]int, allocMem map[string]int, allocCPU map[string]float64, allocStorage map[string]int) {
	gsCount = make(map[string]int)
	allocMem = make(map[string]int)
	allocCPU = make(map[string]float64)
	allocStorage = make(map[string]int)
	gameservers, err := s.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		s.log.Error("listing gameservers for worker stats", "error", err)
		return
	}
	for _, gs := range gameservers {
		if gs.NodeID != nil && *gs.NodeID != "" {
			gsCount[*gs.NodeID]++
			allocMem[*gs.NodeID] += gs.MemoryLimitMB
			allocCPU[*gs.NodeID] += gs.CPULimit
			if gs.StorageLimitMB != nil {
				allocStorage[*gs.NodeID] += *gs.StorageLimitMB
			}
		}
	}
	return
}
