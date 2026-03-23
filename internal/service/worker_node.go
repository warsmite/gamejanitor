package service

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/internal/models"
	"github.com/warsmite/gamejanitor/internal/worker"
)

type WorkerNodeService struct {
	db       *sql.DB
	registry *worker.Registry
	log      *slog.Logger
}

func NewWorkerNodeService(db *sql.DB, registry *worker.Registry, log *slog.Logger) *WorkerNodeService {
	return &WorkerNodeService{db: db, registry: registry, log: log}
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
	PortRangeStart    *int     `json:"port_range_start"`
	PortRangeEnd      *int     `json:"port_range_end"`
	MaxMemoryMB       *int     `json:"max_memory_mb"`
	MaxCPU            *float64 `json:"max_cpu"`
	MaxStorageMB      *int     `json:"max_storage_mb"`
	Cordoned          bool     `json:"cordoned"`
	Tags              []string `json:"tags"`
	Status            string   `json:"status"`
	LastSeen          *string  `json:"last_seen"`
}

func (s *WorkerNodeService) List() ([]WorkerView, error) {
	if s.registry == nil {
		return []WorkerView{}, nil
	}

	infos := s.registry.ListWorkers()
	gsCount, allocMem, allocCPU := s.nodeStats()

	views := make([]WorkerView, 0, len(infos))
	for _, info := range infos {
		node, _ := models.GetWorkerNode(s.db, info.ID)
		views = append(views, s.buildView(info, gsCount[info.ID], allocMem[info.ID], allocCPU[info.ID], node))
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

	gsCount, allocMem, allocCPU := s.nodeStats()
	node, _ := models.GetWorkerNode(s.db, id)
	v := s.buildView(info, gsCount[id], allocMem[id], allocCPU[id], node)
	return &v, nil
}

func (s *WorkerNodeService) SetPortRange(id string, start, end *int) error {
	return models.SetWorkerNodePortRange(s.db, id, start, end)
}

func (s *WorkerNodeService) SetCordoned(id string, cordoned bool) error {
	return models.SetWorkerNodeCordoned(s.db, id, cordoned)
}

func (s *WorkerNodeService) SetLimits(id string, maxMemoryMB *int, maxCPU *float64, maxStorageMB *int) error {
	return models.SetWorkerNodeLimits(s.db, id, maxMemoryMB, maxCPU, maxStorageMB)
}

func (s *WorkerNodeService) SetTags(id string, tags string) error {
	return models.SetWorkerNodeTags(s.db, id, tags)
}

func (s *WorkerNodeService) buildView(info worker.WorkerInfo, gsCount, allocMem int, allocCPU float64, node *models.WorkerNode) WorkerView {
	age := time.Since(info.LastSeen)
	status := "stale"
	if age < 15*time.Second {
		status = "connected"
	} else if age < 25*time.Second {
		status = "slow"
	}

	lastSeen := info.LastSeen.UTC().Format(time.RFC3339)

	v := WorkerView{
		ID:                info.ID,
		LanIP:             info.LanIP,
		ExternalIP:        info.ExternalIP,
		CPUCores:          info.CPUCores,
		MemoryTotalMB:     info.MemoryTotalMB,
		MemoryAvailableMB: info.MemoryAvailableMB,
		GameserverCount:   gsCount,
		AllocatedMemoryMB: allocMem,
		AllocatedCPU:      allocCPU,
		Status:            status,
		LastSeen:          &lastSeen,
	}
	if node != nil {
		v.PortRangeStart = node.PortRangeStart
		v.PortRangeEnd = node.PortRangeEnd
		v.MaxMemoryMB = node.MaxMemoryMB
		v.MaxCPU = node.MaxCPU
		v.MaxStorageMB = node.MaxStorageMB
		v.Cordoned = node.Cordoned
		var tags []string
		if err := json.Unmarshal([]byte(node.Tags), &tags); err == nil {
			v.Tags = tags
		}
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v
}

func (s *WorkerNodeService) nodeStats() (gsCount map[string]int, allocMem map[string]int, allocCPU map[string]float64) {
	gsCount = make(map[string]int)
	allocMem = make(map[string]int)
	allocCPU = make(map[string]float64)
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
		}
	}
	return
}
