package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/go-chi/chi/v5"
)

type ClusterHandlers struct {
	workerSvc *cluster.WorkerNodeService
	log       *slog.Logger
}

func NewClusterHandlers(workerSvc *cluster.WorkerNodeService, log *slog.Logger) *ClusterHandlers {
	return &ClusterHandlers{workerSvc: workerSvc, log: log}
}

type clusterOverview struct {
	Workers            int     `json:"workers"`
	WorkersCordoned    int     `json:"workers_cordoned"`
	TotalMemoryMB      int64   `json:"total_memory_mb"`
	AllocatedMemoryMB  int     `json:"allocated_memory_mb"`
	TotalCPU           float64 `json:"total_cpu"`
	AllocatedCPU       float64 `json:"allocated_cpu"`
	TotalStorageMB     int64   `json:"total_storage_mb"`
	AllocatedStorageMB int     `json:"allocated_storage_mb"`
}

// Get returns the cluster resource summary.
func (h *ClusterHandlers) Get(w http.ResponseWriter, r *http.Request) {
	workers, err := h.workerSvc.List()
	if err != nil {
		h.log.Error("listing workers for cluster overview", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	var overview clusterOverview
	overview.Workers = len(workers)
	for _, w := range workers {
		overview.TotalMemoryMB += w.MemoryTotalMB
		overview.AllocatedMemoryMB += w.AllocatedMemoryMB
		overview.TotalCPU += float64(w.CPUCores)
		overview.AllocatedCPU += w.AllocatedCPU
		overview.TotalStorageMB += w.DiskTotalMB
		overview.AllocatedStorageMB += w.AllocatedStorageMB
		if w.Cordoned {
			overview.WorkersCordoned++
		}
	}

	respondOK(w, overview)
}

// ListWorkers returns all registered worker nodes.
func (h *ClusterHandlers) ListWorkers(w http.ResponseWriter, r *http.Request) {
	views, err := h.workerSvc.List()
	if err != nil {
		h.log.Error("listing workers", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

// GetWorker returns a single worker node.
func (h *ClusterHandlers) GetWorker(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "workerID")
	view, err := h.workerSvc.Get(id)
	if err != nil {
		h.log.Error("getting worker", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

// UpdateWorker updates a worker node (cordon, labels, etc.).
func (h *ClusterHandlers) UpdateWorker(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req cluster.WorkerNodeUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.workerSvc.Update(r.Context(), workerID, &req); err != nil {
		h.log.Error("updating worker", "id", workerID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker updated via API", "worker", workerID)
	view, err := h.workerSvc.Get(workerID)
	if err != nil {
		h.log.Error("getting worker after update", "id", workerID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}
