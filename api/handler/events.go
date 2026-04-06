package handler

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/event"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

// GameserverQuerier provides all gameserver lookups needed by middleware and handlers.
// Implemented by the gameserver store.
type GameserverQuerier interface {
	GetGameserverOwner(gameserverID string) (*string, error)
	GetGameserverGrants(gameserverID string) (model.GrantMap, error)
	ListGameserverIDsByToken(tokenID string) ([]string, error)
	ListGrantedGameserverIDs(tokenID string) ([]string, error)
	CountGameserversByToken(tokenID string) (int, error)
	SumResourcesByToken(tokenID string) (memoryMB int, cpu float64, storageMB int, err error)
}

// visibleGameserverIDs returns owned + granted gameserver IDs for a token.
// Returns nil for admin or no-auth (no filtering needed).
func visibleGameserverIDs(r *http.Request, vis GameserverQuerier) []string {
	token := auth.TokenFromContext(r.Context())
	if token == nil || auth.IsAdmin(token) || vis == nil {
		return nil
	}
	owned, _ := vis.ListGameserverIDsByToken(token.ID)
	granted, _ := vis.ListGrantedGameserverIDs(token.ID)
	return append(owned, granted...)
}

type EventHandlers struct {
	bus        *event.EventBus
	historySvc *event.EventHistoryService
	visibility GameserverQuerier
	log        *slog.Logger
}

func NewEventHandlers(bus *event.EventBus, historySvc *event.EventHistoryService, visibility GameserverQuerier, log *slog.Logger) *EventHandlers {
	return &EventHandlers{bus: bus, historySvc: historySvc, visibility: visibility, log: log}
}

func (h *EventHandlers) SSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Parse type filter — default to all events
	var typeFilter []string // nil = all events
	if types := r.URL.Query().Get("types"); types != "" && types != "*" {
		typeFilter = strings.Split(types, ",")
	}

	// Token scoping — only send events for gameservers the token can access
	allowedIDs := visibleGameserverIDs(r, h.visibility)
	var allowedSet map[string]bool
	if len(allowedIDs) > 0 {
		allowedSet = make(map[string]bool, len(allowedIDs))
		for _, id := range allowedIDs {
			allowedSet[id] = true
		}
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := h.bus.Subscribe()
	defer unsubscribe()

	fmt.Fprint(w, ": heartbeat\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			if !matchesTypeFilter(evt.EventType(), typeFilter) {
				continue
			}
			// Filter by token scope — cluster events (empty gameserver ID) only for all-access tokens
			if allowedSet != nil {
				gsID := evt.EventGameserverID()
				if gsID == "" || !allowedSet[gsID] {
					continue
				}
			}
			data, err := json.Marshal(evt)
			if err != nil {
				h.log.Error("marshaling SSE event", "error", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.EventType(), data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (h *EventHandlers) History(w http.ResponseWriter, r *http.Request) {
	p := parsePagination(r)
	if p.Limit <= 0 {
		p.Limit = PaginationDefaultLimit
	}

	allowedIDs := visibleGameserverIDs(r, h.visibility)

	// If a specific gameserver_id is requested, verify it's in the allowed set
	requestedGSID := r.URL.Query().Get("gameserver_id")
	if requestedGSID != "" && len(allowedIDs) > 0 {
		found := false
		for _, id := range allowedIDs {
			if id == requestedGSID {
				found = true
				break
			}
		}
		if !found {
			respondOK(w, []model.Event{})
			return
		}
	}

	var gsIDFilter *string
	if requestedGSID != "" {
		gsIDFilter = &requestedGSID
	}
	var typeFilter *string
	if v := r.URL.Query().Get("type"); v != "" {
		typeFilter = &v
	}

	events, err := h.historySvc.List(model.EventFilter{
		Type:         typeFilter,
		GameserverID: gsIDFilter,
		Pagination:   p,
	})
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list events")
		return
	}
	respondOK(w, events)
}

// matchesTypeFilter checks if an event type matches any of the filter patterns.
// nil filter means all events pass.
func matchesTypeFilter(eventType string, patterns []string) bool {
	if patterns == nil {
		return true
	}
	for _, p := range patterns {
		if p == "*" {
			return true
		}
		if p == eventType {
			return true
		}
		if matched, _ := path.Match(p, eventType); matched {
			return true
		}
	}
	return false
}
