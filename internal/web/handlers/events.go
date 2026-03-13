package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type EventHandlers struct {
	broadcaster *service.EventBroadcaster
	log         *slog.Logger
}

func NewEventHandlers(broadcaster *service.EventBroadcaster, log *slog.Logger) *EventHandlers {
	return &EventHandlers{broadcaster: broadcaster, log: log}
}

func (h *EventHandlers) SSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unsubscribe := h.broadcaster.Subscribe()
	defer unsubscribe()

	// Initial heartbeat to establish connection
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
			data, err := json.Marshal(event.Data)
			if err != nil {
				h.log.Error("marshaling SSE event", "error", err)
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.Type, data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}
