package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/go-chi/chi/v5"
)

type GameserverHandlers struct {
	svc          *service.GameserverService
	consoleSvc   *service.ConsoleService
	querySvc     *service.QueryService
	statsPoller  *service.StatsPoller
	log          *slog.Logger
}

func NewGameserverHandlers(svc *service.GameserverService, consoleSvc *service.ConsoleService, querySvc *service.QueryService, statsPoller *service.StatsPoller, log *slog.Logger) *GameserverHandlers {
	return &GameserverHandlers{svc: svc, consoleSvc: consoleSvc, querySvc: querySvc, statsPoller: statsPoller, log: log}
}

func (h *GameserverHandlers) List(w http.ResponseWriter, r *http.Request) {
	filter := models.GameserverFilter{
		Pagination: parsePagination(r),
	}
	if game := r.URL.Query().Get("game"); game != "" {
		filter.GameID = &game
	}
	if status := r.URL.Query().Get("status"); status != "" {
		filter.Status = &status
	}

	// Tokens with specific gameserver IDs only see those gameservers
	if token := service.TokenFromContext(r.Context()); token != nil {
		var gsIDs []string
		if err := json.Unmarshal(token.GameserverIDs, &gsIDs); err == nil && len(gsIDs) > 0 {
			filter.IDs = gsIDs
		}
	}

	gameservers, err := h.svc.ListGameservers(filter)
	if err != nil {
		h.log.Error("listing gameservers", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
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
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
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

	rawPassword, err := h.svc.CreateGameserver(r.Context(), &gs)
	if err != nil {
		h.log.Error("creating gameserver", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	// Include the raw SFTP password in the create response only (show once)
	type createResponse struct {
		models.Gameserver
		SFTPPassword string `json:"sftp_password"`
	}
	respondCreated(w, createResponse{Gameserver: gs, SFTPPassword: rawPassword})
}

func (h *GameserverHandlers) RegenerateSFTPPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rawPassword, err := h.svc.RegenerateSFTPPassword(r.Context(), id)
	if err != nil {
		h.log.Error("regenerating sftp password", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, map[string]string{"sftp_password": rawPassword})
}

func (h *GameserverHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var gs models.Gameserver
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	gs.ID = id

	migrationTriggered, err := h.svc.UpdateGameserver(r.Context(), &gs)
	if err != nil {
		h.log.Error("updating gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	// Re-read from DB to get final state
	updated, err := h.svc.GetGameserver(id)
	if err != nil || updated == nil {
		respondOK(w, gs) // fallback to request data
		return
	}
	if migrationTriggered {
		respondOK(w, map[string]any{
			"gameserver":         updated,
			"migration_triggered": true,
		})
		return
	}
	respondOK(w, updated)
}

func (h *GameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.svc.DeleteGameserver(detachedCtx(r), id); err != nil {
		h.log.Error("deleting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}

func (h *GameserverHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Start(detachedCtx(r), id) })
}

func (h *GameserverHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Stop(detachedCtx(r), id) })
}

func (h *GameserverHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Restart(detachedCtx(r), id) })
}

func (h *GameserverHandlers) UpdateServerGame(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.UpdateServerGame(detachedCtx(r), id) })
}

func (h *GameserverHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Reinstall(detachedCtx(r), id) })
}

func (h *GameserverHandlers) Migrate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.NodeID == "" {
		respondError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	if err := h.svc.MigrateGameserver(detachedCtx(r), id, body.NodeID); err != nil {
		h.log.Error("migrating gameserver", "id", id, "target_node", body.NodeID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after migration", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, gs)
}

func (h *GameserverHandlers) BulkAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		NodeID string `json:"node_id"`
		All    bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	actionFn, ok := map[string]func(context.Context, string) error{
		"start":   h.svc.Start,
		"stop":    h.svc.Stop,
		"restart": h.svc.Restart,
	}[body.Action]
	if !ok {
		respondError(w, http.StatusBadRequest, "action must be start, stop, or restart")
		return
	}

	if !body.All && body.NodeID == "" {
		respondError(w, http.StatusBadRequest, "either all or node_id is required")
		return
	}

	filter := models.GameserverFilter{}
	if body.NodeID != "" {
		filter.NodeID = &body.NodeID
	}
	if token := service.TokenFromContext(r.Context()); token != nil {
		var gsIDs []string
		if err := json.Unmarshal(token.GameserverIDs, &gsIDs); err == nil && len(gsIDs) > 0 {
			filter.IDs = gsIDs
		}
	}

	gameservers, err := h.svc.ListGameservers(filter)
	if err != nil {
		h.log.Error("listing gameservers for bulk action", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	type result struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []result
	ctx := r.Context()
	for _, gs := range gameservers {
		r := result{ID: gs.ID, Name: gs.Name}
		if err := actionFn(ctx, gs.ID); err != nil {
			r.Error = err.Error()
			r.Status = gs.Status
		} else {
			updated, _ := h.svc.GetGameserver(gs.ID)
			if updated != nil {
				r.Status = updated.Status
			}
		}
		results = append(results, r)
	}

	h.log.Info("bulk action completed", "action", body.Action, "total", len(gameservers), "node_id", body.NodeID)
	respondOK(w, results)
}

// doAction runs a lifecycle action, then fetches and returns the updated gameserver.
func (h *GameserverHandlers) doAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	id := chi.URLParam(r, "id")
	if err := action(id); err != nil {
		h.log.Error("gameserver action failed", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after action", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, gs)
}

// detachedCtx creates a new context that preserves request values (actor, auth)
// but is not canceled when the HTTP request completes. This allows long-running
// operations like image pulls to survive client disconnects.
func detachedCtx(r *http.Request) context.Context {
	ctx := context.Background()
	// Preserve actor for event publishing
	if actor := service.ActorFromContext(r.Context()); actor.Type != "" {
		ctx = service.SetActorInContext(ctx, actor)
	}
	return ctx
}

type statusResponse struct {
	Status      string         `json:"status"`
	ErrorReason string         `json:"error_reason,omitempty"`
	Container   *containerInfo `json:"container"`
}

type queryInfo struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

type containerInfo struct {
	State     string    `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

func (h *GameserverHandlers) Status(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for status", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}

	resp := statusResponse{
		Status:      gs.Status,
		ErrorReason: gs.ErrorReason,
	}

	if gs.ContainerID != nil {
		info, err := h.svc.GetContainerInfo(r.Context(), id)
		if err != nil {
			h.log.Warn("failed to inspect container for status", "id", id, "error", err)
		} else {
			resp.Container = &containerInfo{
				State:     info.State,
				StartedAt: info.StartedAt,
			}
		}
	}

	respondOK(w, resp)
}

func (h *GameserverHandlers) Query(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	qd := h.querySvc.GetQueryData(id)
	if qd == nil {
		respondOK(w, nil)
		return
	}

	players := make([]string, len(qd.Players))
	for i, p := range qd.Players {
		players[i] = p.Name
	}

	respondOK(w, queryInfo{
		PlayersOnline: qd.PlayersOnline,
		MaxPlayers:    qd.MaxPlayers,
		Players:       players,
		Map:           qd.Map,
		Version:       qd.Version,
	})
}

func (h *GameserverHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Serve from poller cache if available (instant, no Docker call)
	if cached := h.statsPoller.GetCachedStats(id); cached != nil {
		resp := map[string]any{
			"cpu_percent":       cached.CPUPercent,
			"memory_usage_mb":   cached.MemoryUsageMB,
			"memory_limit_mb":   cached.MemoryLimitMB,
			"volume_size_bytes": cached.VolumeSizeBytes,
		}
		if cached.StorageLimitMB != nil {
			resp["storage_limit_mb"] = *cached.StorageLimitMB
		}
		respondOK(w, resp)
		return
	}

	// Fallback: live fetch (poller not running yet)
	stats, err := h.svc.GetGameserverStats(r.Context(), id)
	if err != nil {
		h.log.Warn("failed to get gameserver stats", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), "failed to get gameserver stats")
		return
	}

	resp := map[string]any{
		"cpu_percent":       stats.CPUPercent,
		"memory_usage_mb":   stats.MemoryUsageMB,
		"memory_limit_mb":   stats.MemoryLimitMB,
		"volume_size_bytes": stats.VolumeSizeBytes,
	}
	if stats.StorageLimitMB != nil {
		resp["storage_limit_mb"] = *stats.StorageLimitMB
	}
	respondOK(w, resp)
}

func (h *GameserverHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tail := constants.PaginationDefaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	reader, err := h.svc.GetContainerLogs(r.Context(), id, tail)
	if err != nil {
		// Fall back to historical logs from volume
		lines, histErr := h.consoleSvc.ReadHistoricalLogs(r.Context(), id, 0, tail)
		if histErr != nil {
			h.log.Error("reading logs", "id", id, "live_error", err, "historical_error", histErr)
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}
		if lines == nil {
			lines = []string{}
		}
		respondOK(w, map[string]any{"lines": lines, "historical": true})
		return
	}
	defer reader.Close()

	lines := worker.ParseLogLines(reader)
	respondOK(w, map[string]any{"lines": lines})
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

	output, err := h.consoleSvc.SendCommand(r.Context(), id, strings.TrimSpace(body.Command))
	if err != nil {
		h.log.Error("sending command", "gameserver_id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, map[string]string{"output": output})
}
