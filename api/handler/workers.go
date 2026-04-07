package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/go-chi/chi/v5"
)

type WorkerHandlers struct {
	svc *orchestrator.WorkerNodeService
	log *slog.Logger
}

func NewWorkerHandlers(svc *orchestrator.WorkerNodeService, log *slog.Logger) *WorkerHandlers {
	return &WorkerHandlers{svc: svc, log: log}
}

func (h *WorkerHandlers) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List()
	if err != nil {
		h.log.Error("listing workers", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WorkerHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "workerID")
	view, err := h.svc.Get(id)
	if err != nil {
		h.log.Error("getting worker", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

func (h *WorkerHandlers) Update(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	var req orchestrator.WorkerNodeUpdate
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if err := h.svc.Update(r.Context(), workerID, &req); err != nil {
		h.log.Error("updating worker", "id", workerID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	h.log.Info("worker updated via API", "worker", workerID)
	view, err := h.svc.Get(workerID)
	if err != nil {
		h.log.Error("getting worker after update", "id", workerID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}
