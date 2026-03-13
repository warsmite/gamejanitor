package handlers

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type GameserverHandlers struct {
	svc        *service.GameserverService
	consoleSvc *service.ConsoleService
	querySvc   *service.QueryService
	docker     *docker.Client
	log        *slog.Logger
}

func NewGameserverHandlers(svc *service.GameserverService, consoleSvc *service.ConsoleService, querySvc *service.QueryService, dockerClient *docker.Client, log *slog.Logger) *GameserverHandlers {
	return &GameserverHandlers{svc: svc, consoleSvc: consoleSvc, querySvc: querySvc, docker: dockerClient, log: log}
}

func (h *GameserverHandlers) List(w http.ResponseWriter, r *http.Request) {
	filter := models.GameserverFilter{}
	if game := r.URL.Query().Get("game"); game != "" {
		filter.GameID = &game
	}
	if status := r.URL.Query().Get("status"); status != "" {
		filter.Status = &status
	}

	gameservers, err := h.svc.ListGameservers(filter)
	if err != nil {
		h.log.Error("listing gameservers", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gameservers == nil {
		gameservers = []models.Gameserver{}
	}
	respondOK(w, gameservers)
}

func (h *GameserverHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver", "id", id, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	respondOK(w, gs)
}

func (h *GameserverHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var gs models.Gameserver
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if gs.Name == "" || gs.GameID == "" {
		respondError(w, http.StatusBadRequest, "name and game_id are required")
		return
	}

	if err := h.svc.CreateGameserver(r.Context(), &gs); err != nil {
		h.log.Error("creating gameserver", "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}
	respondCreated(w, gs)
}

func (h *GameserverHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var gs models.Gameserver
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	gs.ID = id

	if err := h.svc.UpdateGameserver(&gs); err != nil {
		h.log.Error("updating gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}
	respondOK(w, gs)
}

func (h *GameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteGameserver(r.Context(), id); err != nil {
		h.log.Error("deleting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}
	respondNoContent(w)
}

func (h *GameserverHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Start(r.Context(), id) })
}

func (h *GameserverHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Stop(r.Context(), id) })
}

func (h *GameserverHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Restart(r.Context(), id) })
}

func (h *GameserverHandlers) UpdateServerGame(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.UpdateServerGame(r.Context(), id) })
}

func (h *GameserverHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Reinstall(r.Context(), id) })
}

// doAction runs a lifecycle action, then fetches and returns the updated gameserver.
func (h *GameserverHandlers) doAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	id := chi.URLParam(r, "id")
	if err := action(id); err != nil {
		h.log.Error("gameserver action failed", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}

	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after action", "id", id, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondOK(w, gs)
}

type statusResponse struct {
	Status    string         `json:"status"`
	Container *containerInfo `json:"container"`
	Query     *queryInfo     `json:"query"`
}

type queryInfo struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

type containerInfo struct {
	State         string    `json:"state"`
	StartedAt     time.Time `json:"started_at"`
	MemoryUsageMB int       `json:"memory_usage_mb"`
	MemoryLimitMB int       `json:"memory_limit_mb"`
	CPUPercent    float64   `json:"cpu_percent"`
}

func (h *GameserverHandlers) Status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for status", "id", id, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}

	resp := statusResponse{
		Status: gs.Status,
	}

	if qd := h.querySvc.GetQueryData(id); qd != nil {
		players := make([]string, len(qd.Players))
		for i, p := range qd.Players {
			players[i] = p.Name
		}
		resp.Query = &queryInfo{
			PlayersOnline: qd.PlayersOnline,
			MaxPlayers:    qd.MaxPlayers,
			Players:       players,
			Map:           qd.Map,
			Version:       qd.Version,
		}
	}

	if gs.ContainerID != nil {
		info, err := h.docker.InspectContainer(r.Context(), *gs.ContainerID)
		if err != nil {
			h.log.Warn("failed to inspect container for status", "id", id, "error", err)
		} else {
			ci := &containerInfo{
				State:     info.State,
				StartedAt: info.StartedAt,
			}

			stats, err := h.docker.ContainerStats(r.Context(), *gs.ContainerID)
			if err != nil {
				h.log.Warn("failed to get container stats", "id", id, "error", err)
			} else {
				ci.MemoryUsageMB = stats.MemoryUsageMB
				ci.MemoryLimitMB = stats.MemoryLimitMB
				ci.CPUPercent = stats.CPUPercent
			}

			resp.Container = ci
		}
	}

	respondOK(w, resp)
}

func (h *GameserverHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for logs", "id", id, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if gs.ContainerID == nil {
		respondError(w, http.StatusBadRequest, "gameserver has no container")
		return
	}

	tail := 100
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	reader, err := h.docker.ContainerLogs(r.Context(), *gs.ContainerID, tail, false)
	if err != nil {
		h.log.Error("reading container logs", "id", id, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer reader.Close()

	lines := readDockerLogs(reader)
	respondOK(w, map[string]any{"lines": lines})
}

// readDockerLogs parses Docker's multiplexed log stream into plain text lines.
// Docker log format: [stream_type(1)][padding(3)][size(4)][payload(size)]
func readDockerLogs(r io.Reader) []string {
	br := bufio.NewReaderSize(r, 32*1024)
	header := make([]byte, 8)
	var lines []string

	for {
		_, err := io.ReadFull(br, header)
		if err != nil {
			break
		}

		streamType := header[0]
		frameSize := binary.BigEndian.Uint32(header[4:8])
		if frameSize == 0 {
			continue
		}

		payload := make([]byte, frameSize)
		if _, err := io.ReadFull(br, payload); err != nil {
			break
		}

		text := strings.TrimRight(string(payload), "\n")
		prefix := ""
		if streamType == 2 {
			prefix = "[ERR] "
		}

		for _, line := range strings.Split(text, "\n") {
			if line != "" {
				lines = append(lines, fmt.Sprintf("%s%s", prefix, line))
			}
		}
	}

	return lines
}

func (h *GameserverHandlers) SendCommand(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var body struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Command) == "" {
		respondError(w, http.StatusBadRequest, "command is required")
		return
	}

	if err := h.consoleSvc.SendCommand(r.Context(), id, strings.TrimSpace(body.Command)); err != nil {
		h.log.Error("sending command", "gameserver_id", id, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}

	respondNoContent(w)
}
