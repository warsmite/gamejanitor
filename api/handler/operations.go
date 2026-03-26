package handler

import (
	"net/http"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/controller/auth"
)

// OperationStore abstracts operation queries for the API handler.
type OperationStore interface {
	ListOperations(filter model.OperationFilter) ([]model.Operation, error)
}

type OperationHandlers struct {
	store OperationStore
}

func NewOperationHandlers(store OperationStore) *OperationHandlers {
	return &OperationHandlers{store: store}
}

// List returns operations, optionally filtered by gameserver_id, status, or worker_id.
func (h *OperationHandlers) List(w http.ResponseWriter, r *http.Request) {
	filter := model.OperationFilter{}
	if v := r.URL.Query().Get("gameserver_id"); v != "" {
		filter.GameserverID = &v
	}
	if v := r.URL.Query().Get("status"); v != "" {
		filter.Status = &v
	}
	if v := r.URL.Query().Get("worker_id"); v != "" {
		filter.WorkerID = &v
	}

	// Scope to allowed gameservers for non-admin tokens
	allowedIDs := auth.AllowedGameserverIDs(auth.TokenFromContext(r.Context()))
	if len(allowedIDs) > 0 && filter.GameserverID == nil {
		// Non-admin without specific gameserver filter — return empty rather than all
		respondOK(w, []model.Operation{})
		return
	}

	ops, err := h.store.ListOperations(filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list operations")
		return
	}
	respondOK(w, ops)
}
