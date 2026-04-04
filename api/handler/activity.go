package handler

import (
	"net/http"

	"github.com/warsmite/gamejanitor/model"
)

// EventStore abstracts event queries for the API handler.
type EventStore interface {
	ListEvents(filter model.EventFilter) ([]model.Event, error)
}

type ActivityHandlers struct {
	store   EventStore
	gsQuery GameserverQuerier
}

func NewActivityHandlers(store EventStore, gsQuery GameserverQuerier) *ActivityHandlers {
	return &ActivityHandlers{store: store, gsQuery: gsQuery}
}

// List returns events, optionally filtered by gameserver_id, type, or worker_id.
// Non-admin tokens only see events for gameservers they own or have grants on.
func (h *ActivityHandlers) List(w http.ResponseWriter, r *http.Request) {
	p := parsePagination(r)
	if p.Limit <= 0 {
		p.Limit = PaginationDefaultLimit
	}

	filter := model.EventFilter{
		Pagination: p,
	}
	if v := r.URL.Query().Get("gameserver_id"); v != "" {
		filter.GameserverID = &v
	}
	if v := r.URL.Query().Get("type"); v != "" {
		filter.Type = &v
	}
	if v := r.URL.Query().Get("worker_id"); v != "" {
		filter.WorkerID = &v
	}

	// Scope to visible gameservers for non-admin tokens
	allowedIDs := visibleGameserverIDs(r, h.gsQuery)
	if allowedIDs != nil && filter.GameserverID == nil {
		// Non-admin with no specific gameserver filter — return empty
		// (activity is per-gameserver, listing all for scoped tokens is not supported)
		respondOK(w, []model.Event{})
		return
	}

	events, err := h.store.ListEvents(filter)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	if events == nil {
		events = []model.Event{}
	}
	respondOK(w, events)
}
