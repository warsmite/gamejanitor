package handlers

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

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
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for console", "game_id", gs.GameID, "error", err)
		http.Error(w, "Failed to load game", http.StatusInternalServerError)
		return
	}

	canRead := game != nil && service.HasCapability(game, "console_read")
	canSend := game != nil && service.HasCapability(game, "console_send")
	isRunning := gs.Status == "started" || gs.Status == "running"

	h.renderer.Render(w, r, "gameservers/console", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"CanRead":    canRead,
		"CanSend":    canSend,
		"IsRunning":  isRunning,
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
		http.Error(w, err.Error(), http.StatusBadRequest)
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
		parseDockerLogStream(reader, lines)
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
				fmt.Fprintf(w, "data: %s\n", html.EscapeString(part))
			}
			fmt.Fprint(w, "\n")
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// parseDockerLogStream reads the Docker multiplexed log format and sends individual lines to the channel.
// Docker log format: [stream_type(1)][0(3)][size(4)][payload(size)]
// stream_type: 1=stdout, 2=stderr
func parseDockerLogStream(r io.Reader, lines chan<- string) {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)

	for {
		_, err := io.ReadFull(br, header)
		if err != nil {
			return
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		_, err = io.ReadFull(br, payload)
		if err != nil {
			return
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines <- prefix + line
			}
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(output))
}
