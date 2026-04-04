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
	store EventStore
}

func NewActivityHandlers(store EventStore) *ActivityHandlers {
	return &ActivityHandlers{store: store}
}

// List returns events, optionally filtered by gameserver_id, type, or worker_id.
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

	// TODO: scope activity to owned + granted gameservers for non-admin tokens

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
