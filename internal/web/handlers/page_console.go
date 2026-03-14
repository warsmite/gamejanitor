package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageConsoleHandlers struct {
	consoleSvc    *service.ConsoleService
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageConsoleHandlers(consoleSvc *service.ConsoleService, gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageConsoleHandlers {
	return &PageConsoleHandlers{consoleSvc: consoleSvc, gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
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

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for console", "game_id", gs.GameID, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}

	canRead := game != nil && service.HasCapability(game, "console_read")
	canSend := game != nil && service.HasCapability(game, "console_send")

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

	reader, err := h.consoleSvc.StreamLogs(r.Context(), id, 200)
	if err != nil {
		h.log.Error("starting log stream", "gameserver_id", id, "error", err)
		http.Error(w, "Failed to start log stream", http.StatusInternalServerError)
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
		docker.ParseLogStream(reader, lines)
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
