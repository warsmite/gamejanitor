package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/go-chi/chi/v5"
)

type PageConsoleHandlers struct {
	consoleSvc    *service.ConsoleService
	gameStore     *games.GameStore
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageConsoleHandlers(consoleSvc *service.ConsoleService, gameStore *games.GameStore, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageConsoleHandlers {
	return &PageConsoleHandlers{consoleSvc: consoleSvc, gameStore: gameStore, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageConsoleHandlers) Console(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for console", "id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if gs == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
		return
	}

	game := h.gameStore.GetGame(gs.GameID)

	canRead := game != nil && service.HasCapability(game, "console_read")
	canSend := game != nil && service.HasCapability(game, "command")

	h.renderer.Render(w, r, "gameservers/console", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"CanRead":    canRead,
		"CanSend":    canSend,
	})
}

func (h *PageConsoleHandlers) LogStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// If a specific session is requested, serve historical logs directly
	if r.URL.Query().Get("session") != "" {
		h.serveHistoricalLogs(w, r, id, flusher)
		return
	}

	reader, err := h.consoleSvc.StreamLogs(r.Context(), id, 200)
	if err != nil {
		// No live stream available — try historical logs
		h.serveHistoricalLogs(w, r, id, flusher)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Initial heartbeat
	fmt.Fprint(w, ": heartbeat\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// Channel for parsed log lines from the Docker multiplexed stream
	lines := make(chan string, 64)
	done := make(chan struct{})

	go func() {
		defer close(lines)
		defer close(done)
		worker.ParseLogStream(reader, lines)
	}()

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-lines:
			if !ok {
				fmt.Fprint(w, "event: eof\ndata: stream ended\n\n")
				flusher.Flush()
				return
			}
			// SSE data lines cannot contain newlines — split if needed
			for _, part := range strings.Split(line, "\n") {
				fmt.Fprintf(w, "data: %s\n", part)
			}
			fmt.Fprint(w, "\n")
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (h *PageConsoleHandlers) serveHistoricalLogs(w http.ResponseWriter, r *http.Request, id string, flusher http.Flusher) {
	session := 0
	if v := r.URL.Query().Get("session"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			session = n
		}
	}

	lines, err := h.consoleSvc.ReadHistoricalLogs(r.Context(), id, session, 200)
	if err != nil {
		h.log.Error("reading historical logs", "gameserver_id", id, "error", err)
		http.Error(w, "Failed to read logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	fmt.Fprint(w, "event: historical\ndata: true\n\n")
	flusher.Flush()

	for _, line := range lines {
		for _, part := range strings.Split(line, "\n") {
			fmt.Fprintf(w, "data: %s\n", part)
		}
		fmt.Fprint(w, "\n")
		flusher.Flush()
	}

	fmt.Fprint(w, "event: eof\ndata: stream ended\n\n")
	flusher.Flush()
}

func (h *PageConsoleHandlers) Sessions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	sessions, err := h.consoleSvc.ListLogSessions(r.Context(), id)
	if err != nil {
		h.log.Error("listing log sessions", "gameserver_id", id, "error", err)
		http.Error(w, "Failed to list sessions", http.StatusInternalServerError)
		return
	}
	if sessions == nil {
		sessions = []service.LogSession{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sessions)
}

func (h *PageConsoleHandlers) SendCommand(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	command := strings.TrimSpace(r.FormValue("command"))
	if command == "" {
		http.Error(w, "Command is required", http.StatusBadRequest)
		return
	}

	output, err := h.consoleSvc.SendCommand(r.Context(), id, command)
	if err != nil {
		h.log.Error("sending command", "gameserver_id", id, "command", command, "error", err)
		http.Error(w, "Failed to send command", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}
