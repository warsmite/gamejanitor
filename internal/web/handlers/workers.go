package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type WorkerHandlers struct {
	svc *service.WorkerNodeService
	log *slog.Logger
}

func NewWorkerHandlers(svc *service.WorkerNodeService, log *slog.Logger) *WorkerHandlers {
	return &WorkerHandlers{svc: svc, log: log}
}

func (h *WorkerHandlers) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List()
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WorkerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.Get(chi.URLParam(r, "workerID"))
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

func (h *WorkerHandlers) respondWithWorker(w http.ResponseWriter, workerID string) {
	view, err := h.svc.Get(workerID)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
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

	if err := h.svc.SetPortRange(workerID, &req.PortRangeStart, &req.PortRangeEnd); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker port range set via API", "worker_id", workerID, "start", req.PortRangeStart, "end", req.PortRangeEnd)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) ClearPortRange(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := h.svc.SetPortRange(workerID, nil, nil); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	h.log.Info("worker port range cleared via API", "worker_id", workerID)
	h.respondWithWorker(w, workerID)
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

	if err := h.svc.SetLimits(workerID, req.MaxMemoryMB, req.MaxCPU, req.MaxStorageMB); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker limits set via API", "worker_id", workerID, "max_memory_mb", req.MaxMemoryMB, "max_cpu", req.MaxCPU, "max_storage_mb", req.MaxStorageMB)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) ClearLimits(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := h.svc.SetLimits(workerID, nil, nil, nil); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	h.log.Info("worker limits cleared via API", "worker_id", workerID)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) Cordon(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := h.svc.SetCordoned(workerID, true); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	h.log.Info("worker cordoned via API", "worker_id", workerID)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) Uncordon(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := h.svc.SetCordoned(workerID, false); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	h.log.Info("worker uncordoned via API", "worker_id", workerID)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) SetTags(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	tagsJSON, _ := json.Marshal(req.Tags)
	if err := h.svc.SetTags(workerID, string(tagsJSON)); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker tags set via API", "worker_id", workerID, "tags", req.Tags)
	h.respondWithWorker(w, workerID)
}

func (h *WorkerHandlers) ClearTags(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := h.svc.SetTags(workerID, "[]"); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	h.log.Info("worker tags cleared via API", "worker_id", workerID)
	h.respondWithWorker(w, workerID)
}
