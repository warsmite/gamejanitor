package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/go-chi/chi/v5"
)

type WorkerHandlers struct {
	registry      *worker.Registry
	workerNodeSvc *service.WorkerNodeService
	gameserverSvc *service.GameserverService
	log           *slog.Logger
}

func NewWorkerHandlers(registry *worker.Registry, workerNodeSvc *service.WorkerNodeService, gameserverSvc *service.GameserverService, log *slog.Logger) *WorkerHandlers {
	return &WorkerHandlers{registry: registry, workerNodeSvc: workerNodeSvc, gameserverSvc: gameserverSvc, log: log}
}

type workerAPIView struct {
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
	Status            string   `json:"status"`
	LastSeen          *string  `json:"last_seen"`
}

func (h *WorkerHandlers) buildWorkerView(info worker.WorkerInfo, gsCount, allocMem int, allocCPU float64, node *models.WorkerNode) workerAPIView {
	age := time.Since(info.LastSeen)
	status := "stale"
	if age < 15*time.Second {
		status = "connected"
	} else if age < 25*time.Second {
		status = "slow"
	}

	lastSeen := info.LastSeen.UTC().Format(time.RFC3339)

	v := workerAPIView{
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
	}
	return v
}

func (h *WorkerHandlers) nodeStats() (gsCount map[string]int, allocMem map[string]int, allocCPU map[string]float64) {
	gsCount = make(map[string]int)
	allocMem = make(map[string]int)
	allocCPU = make(map[string]float64)
	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for worker stats", "error", err)
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

func (h *WorkerHandlers) List(w http.ResponseWriter, r *http.Request) {
	if h.registry == nil {
		respondOK(w, []workerAPIView{})
		return
	}

	infos := h.registry.ListWorkers()
	gsCount, allocMem, allocCPU := h.nodeStats()

	views := make([]workerAPIView, 0, len(infos))
	for _, info := range infos {
		var node *models.WorkerNode
		if n, err := h.workerNodeSvc.GetWorkerNode(info.ID); err == nil {
			node = n
		}
		views = append(views, h.buildWorkerView(info, gsCount[info.ID], allocMem[info.ID], allocCPU[info.ID], node))
	}
	respondOK(w, views)
}

func (h *WorkerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if h.registry == nil {
		respondError(w, http.StatusNotFound, "multi-node not enabled")
		return
	}

	info, ok := h.registry.GetInfo(workerID)
	if !ok {
		respondError(w, http.StatusNotFound, "worker not found: "+workerID)
		return
	}

	gsCount, allocMem, allocCPU := h.nodeStats()
	var node *models.WorkerNode
	if n, err := h.workerNodeSvc.GetWorkerNode(workerID); err == nil {
		node = n
	}

	respondOK(w, h.buildWorkerView(info, gsCount[workerID], allocMem[workerID], allocCPU[workerID], node))
}

// getWorkerAndRespond is a helper that looks up a worker and returns its updated view.
// Returns false if it already wrote an error response.
func (h *WorkerHandlers) getWorkerAndRespond(w http.ResponseWriter, workerID string) {
	info, ok := h.registry.GetInfo(workerID)
	if !ok {
		respondError(w, http.StatusNotFound, "worker not found: "+workerID)
		return
	}
	gsCount, allocMem, allocCPU := h.nodeStats()
	var node *models.WorkerNode
	if n, err := h.workerNodeSvc.GetWorkerNode(workerID); err == nil {
		node = n
	}
	respondOK(w, h.buildWorkerView(info, gsCount[workerID], allocMem[workerID], allocCPU[workerID], node))
}

func (h *WorkerHandlers) SetPortRange(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req struct {
		PortRangeStart int `json:"port_range_start"`
		PortRangeEnd   int `json:"port_range_end"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.PortRangeStart < 1024 || req.PortRangeStart > 65535 {
		respondError(w, http.StatusBadRequest, "port_range_start must be 1024-65535")
		return
	}
	if req.PortRangeEnd < 1024 || req.PortRangeEnd > 65535 {
		respondError(w, http.StatusBadRequest, "port_range_end must be 1024-65535")
		return
	}
	if req.PortRangeEnd <= req.PortRangeStart {
		respondError(w, http.StatusBadRequest, "port_range_end must be greater than port_range_start")
		return
	}

	if err := h.workerNodeSvc.SetWorkerNodePortRange(workerID, &req.PortRangeStart, &req.PortRangeEnd); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker port range set via API", "worker_id", workerID, "start", req.PortRangeStart, "end", req.PortRangeEnd)
	h.getWorkerAndRespond(w, workerID)
}

func (h *WorkerHandlers) ClearPortRange(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodePortRange(workerID, nil, nil); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker port range cleared via API", "worker_id", workerID)
	h.getWorkerAndRespond(w, workerID)
}

func (h *WorkerHandlers) SetLimits(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req struct {
		MaxMemoryMB  *int     `json:"max_memory_mb"`
		MaxCPU       *float64 `json:"max_cpu"`
		MaxStorageMB *int     `json:"max_storage_mb"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.workerNodeSvc.SetWorkerNodeLimits(workerID, req.MaxMemoryMB, req.MaxCPU, req.MaxStorageMB); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker limits set via API", "worker_id", workerID, "max_memory_mb", req.MaxMemoryMB, "max_cpu", req.MaxCPU, "max_storage_mb", req.MaxStorageMB)
	h.getWorkerAndRespond(w, workerID)
}

func (h *WorkerHandlers) ClearLimits(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeLimits(workerID, nil, nil, nil); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker limits cleared via API", "worker_id", workerID)
	h.getWorkerAndRespond(w, workerID)
}

func (h *WorkerHandlers) Cordon(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeCordoned(workerID, true); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker cordoned via API", "worker_id", workerID)
	h.getWorkerAndRespond(w, workerID)
}

func (h *WorkerHandlers) Uncordon(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeCordoned(workerID, false); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.log.Info("worker uncordoned via API", "worker_id", workerID)
	h.getWorkerAndRespond(w, workerID)
}
