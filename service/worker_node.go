package service

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/validate"
	"github.com/warsmite/gamejanitor/worker"
)

type WorkerNodeService struct {
	db          *sql.DB
	registry    *worker.Registry
	broadcaster *EventBus
	log         *slog.Logger
}

func NewWorkerNodeService(db *sql.DB, registry *worker.Registry, broadcaster *EventBus, log *slog.Logger) *WorkerNodeService {
	return &WorkerNodeService{db: db, registry: registry, broadcaster: broadcaster, log: log}
}

// WorkerView is the enriched API representation of a worker node.
type WorkerView struct {
	ID                string   `json:"id"`
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
	Cordoned          bool     `json:"cordoned"`
	Tags              models.Labels `json:"tags"`
	Status            string   `json:"status"`
	LastSeen          *string  `json:"last_seen"`
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
		node, _ := models.GetWorkerNode(s.db, info.ID)
		views = append(views, s.buildView(info, gsCount[info.ID], allocMem[info.ID], allocCPU[info.ID], allocStorage[info.ID], node))
	}
	return views, nil
}

func (s *WorkerNodeService) Get(id string) (*WorkerView, error) {
	if s.registry == nil {
		return nil, ErrNotFound("multi-node not enabled")
	}

	info, ok := s.registry.GetInfo(id)
	if !ok {
		return nil, ErrNotFoundf("worker %s not found", id)
	}

	gsCount, allocMem, allocCPU, allocStorage := s.nodeStats()
	node, _ := models.GetWorkerNode(s.db, id)
	v := s.buildView(info, gsCount[id], allocMem[id], allocCPU[id], allocStorage[id], node)
	return &v, nil
}

// WorkerNodeUpdate represents a partial update to a worker node.
// Nil pointer fields are not updated. To clear a limit, set it to a pointer to 0.
type WorkerNodeUpdate struct {
	MaxMemoryMB  *int      `json:"max_memory_mb,omitempty"`
	MaxCPU       *float64  `json:"max_cpu,omitempty"`
	MaxStorageMB *int      `json:"max_storage_mb,omitempty"`
	Cordoned     *bool     `json:"cordoned,omitempty"`
	Tags         *models.Labels `json:"tags,omitempty"`
}

func (u *WorkerNodeUpdate) Validate() error {
	var fe validate.FieldErrors
	fe.MinIntPtr("max_memory_mb", u.MaxMemoryMB, 0)
	fe.MinFloatPtr("max_cpu", u.MaxCPU, 0)
	fe.MinIntPtr("max_storage_mb", u.MaxStorageMB, 0)
	return fe.Err()
}

func (s *WorkerNodeService) Update(ctx context.Context, id string, update *WorkerNodeUpdate) error {
	if err := update.Validate(); err != nil {
		return err
	}

	if update.MaxMemoryMB != nil || update.MaxCPU != nil || update.MaxStorageMB != nil {
		if err := models.SetWorkerNodeLimits(s.db, id, update.MaxMemoryMB, update.MaxCPU, update.MaxStorageMB); err != nil {
			return err
		}
	}
	if update.Cordoned != nil {
		if err := models.SetWorkerNodeCordoned(s.db, id, *update.Cordoned); err != nil {
			return err
		}
	}
	if update.Tags != nil {
		if err := models.SetWorkerNodeTags(s.db, id, *update.Tags); err != nil {
			return err
		}
	}

	view, _ := s.Get(id)
	s.broadcaster.Publish(WorkerActionEvent{
		Type:      EventWorkerUpdated,
		Timestamp: time.Now(),
		Actor:     ActorFromContext(ctx),
		WorkerID:  id,
		Worker:    view,
	})

	return nil
}

func (s *WorkerNodeService) buildView(info worker.WorkerInfo, gsCount, allocMem int, allocCPU float64, allocStorage int, node *models.WorkerNode) WorkerView {
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
		v.MaxMemoryMB = node.MaxMemoryMB
		v.MaxCPU = node.MaxCPU
		v.MaxStorageMB = node.MaxStorageMB
		v.Cordoned = node.Cordoned
		v.Tags = node.Tags
	}
	if v.Tags == nil {
		v.Tags = models.Labels{}
	}
	return v
}

func (s *WorkerNodeService) nodeStats() (gsCount map[string]int, allocMem map[string]int, allocCPU map[string]float64, allocStorage map[string]int) {
	gsCount = make(map[string]int)
	allocMem = make(map[string]int)
	allocCPU = make(map[string]float64)
	allocStorage = make(map[string]int)
	gameservers, err := models.ListGameservers(s.db, models.GameserverFilter{})
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
