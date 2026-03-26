package handler

import (
	"net/http"

	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/model"
)

// ActivityStore abstracts activity queries for the API handler.
type ActivityStore interface {
	ListActivities(filter model.ActivityFilter) ([]model.Activity, error)
}

type ActivityHandlers struct {
	store ActivityStore
}

func NewActivityHandlers(store ActivityStore) *ActivityHandlers {
	return &ActivityHandlers{store: store}
}

// List returns activities, optionally filtered by gameserver_id, type, status, or worker_id.
func (h *ActivityHandlers) List(w http.ResponseWriter, r *http.Request) {
	p := parsePagination(r)
	if p.Limit <= 0 {
		p.Limit = PaginationDefaultLimit
	}

	filter := model.ActivityFilter{
		Pagination: p,
	}
	if v := r.URL.Query().Get("gameserver_id"); v != "" {
		filter.GameserverID = &v
	}
	if v := r.URL.Query().Get("type"); v != "" {
		filter.Type = &v
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
		respondOK(w, []model.Activity{})
		return
	}

	activities, err := h.store.ListActivities(filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list activities")
		return
	}
	if activities == nil {
		activities = []model.Activity{}
	}
	respondOK(w, activities)
}
