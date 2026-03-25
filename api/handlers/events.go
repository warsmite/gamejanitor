package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
)

type EventHandlers struct {
	bus        *service.EventBus
	historySvc *service.EventHistoryService
	log        *slog.Logger
}

func NewEventHandlers(bus *service.EventBus, historySvc *service.EventHistoryService, log *slog.Logger) *EventHandlers {
	return &EventHandlers{bus: bus, historySvc: historySvc, log: log}
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
	allowedIDs := service.AllowedGameserverIDs(service.TokenFromContext(r.Context()))
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
		case event, ok := <-ch:
			if !ok {
				return
			}
			if !matchesTypeFilter(event.EventType(), typeFilter) {
				continue
			}
			// Filter by token scope — cluster events (empty gameserver ID) only for all-access tokens
			if allowedSet != nil {
				gsID := event.EventGameserverID()
				if gsID == "" || !allowedSet[gsID] {
					continue
				}
			}
			data, err := json.Marshal(event)
			if err != nil {
				h.log.Error("marshaling SSE event", "error", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.EventType(), data)
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
		p.Limit = constants.PaginationDefaultLimit
	}

	allowedIDs := service.AllowedGameserverIDs(service.TokenFromContext(r.Context()))

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
			respondOK(w, []models.Event{})
			return
		}
	}

	events, err := h.historySvc.List(models.EventFilter{
		EventType:            r.URL.Query().Get("type"),
		GameserverID:         requestedGSID,
		AllowedGameserverIDs: allowedIDs,
		Pagination:           p,
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
