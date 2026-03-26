package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/service"
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

func (h *WorkerHandlers) Update(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req service.WorkerNodeUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.svc.Update(r.Context(), workerID, &req); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker updated via API", "worker_id", workerID)
	view, err := h.svc.Get(workerID)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}
