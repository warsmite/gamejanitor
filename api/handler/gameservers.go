package handler

import (
	"github.com/warsmite/gamejanitor/controller"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker/logparse"
	"github.com/go-chi/chi/v5"
)

// StatsHistoryQuerier reads historical stats from the database.
type StatsHistoryQuerier interface {
	QueryHistory(gameserverID string, period model.StatsPeriod) ([]model.StatsSample, error)
}

type GameserverHandlers struct {
	svc          *gameserver.GameserverService
	consoleSvc   *gameserver.ConsoleService
	querySvc     *status.QueryService
	statsPoller  *status.StatsPoller
	statsHistory StatsHistoryQuerier
	log          *slog.Logger
}

func NewGameserverHandlers(svc *gameserver.GameserverService, consoleSvc *gameserver.ConsoleService, querySvc *status.QueryService, statsPoller *status.StatsPoller, statsHistory StatsHistoryQuerier, log *slog.Logger) *GameserverHandlers {
	return &GameserverHandlers{svc: svc, consoleSvc: consoleSvc, querySvc: querySvc, statsPoller: statsPoller, statsHistory: statsHistory, log: log}
}

func (h *GameserverHandlers) List(w http.ResponseWriter, r *http.Request) {
	filter := model.GameserverFilter{
		Pagination: parsePagination(r),
	}
	if game := r.URL.Query().Get("game"); game != "" {
		filter.GameID = &game
	}
	if status := r.URL.Query().Get("status"); status != "" {
		filter.Status = &status
	}
	if ids := r.URL.Query().Get("ids"); ids != "" {
		for _, id := range strings.Split(ids, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				filter.IDs = append(filter.IDs, id)
			}
		}
	}

	gameservers, err := h.svc.ListGameservers(r.Context(), filter)
	if err != nil {
		h.log.Error("listing gameservers", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if gameservers == nil {
		gameservers = []model.Gameserver{}
	}

	// Include the token's effective permissions so the UI can show/hide controls
	permissions := effectivePermissions(r)
	respondOK(w, map[string]any{
		"gameservers": gameservers,
		"permissions": permissions,
	})
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
	var gs model.Gameserver
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	rawPassword, err := h.svc.CreateGameserver(r.Context(), &gs)
	if err != nil {
		h.log.Error("creating gameserver", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	// Re-fetch so DeriveStatus populates the status field
	fetched, err := h.svc.GetGameserver(gs.ID)
	if err != nil || fetched == nil {
		h.log.Error("fetching gameserver after create", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to fetch created gameserver")
		return
	}

	// Include the raw SFTP password in the create response only (show once)
	type createResponse struct {
		model.Gameserver
		SFTPPassword string `json:"sftp_password"`
	}
	respondCreated(w, createResponse{Gameserver: *fetched, SFTPPassword: rawPassword})
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
	var gs model.Gameserver
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
	w.WriteHeader(http.StatusAccepted)
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

func (h *GameserverHandlers) Archive(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.svc.Archive(detachedCtx(r), id) })
}

func (h *GameserverHandlers) Unarchive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	// Body is optional — empty body means auto-place
	json.NewDecoder(r.Body).Decode(&body)

	h.doAction(w, r, func(_ string) error { return h.svc.Unarchive(detachedCtx(r), id, body.NodeID) })
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

	// Run migration in background — return 202 immediately.
	// Migration involves stopping, volume transfer, and node reassignment which can take minutes.
	// The gameserver.migrate event fires at start, gameserver.error or instance_stopped on completion.
	ctx := detachedCtx(r)
	go func() {
		if err := h.svc.MigrateGameserver(ctx, id, body.NodeID); err != nil {
			h.log.Error("background migration failed", "id", id, "target_node", body.NodeID, "error", err)
		}
	}()

	w.WriteHeader(http.StatusAccepted)
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

	filter := model.GameserverFilter{}
	if body.NodeID != "" {
		filter.NodeID = &body.NodeID
	}

	gameservers, err := h.svc.ListGameservers(r.Context(), filter)
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
	if actor := controller.ActorFromContext(r.Context()); actor.Type != "" {
		ctx = controller.SetActorInContext(ctx, actor)
	}
	return ctx
}

type statusResponse struct {
	Status      string         `json:"status"`
	ErrorReason string         `json:"error_reason,omitempty"`
	Instance    *instanceInfo `json:"instance"`
}

type queryInfo struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

type instanceInfo struct {
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

	if gs.InstanceID != nil {
		info, err := h.svc.GetInstanceInfo(r.Context(), id)
		if err != nil {
			h.log.Warn("failed to inspect instance for status", "id", id, "error", err)
		} else {
			resp.Instance = &instanceInfo{
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
			"net_rx_bytes":      cached.NetRxBytes,
			"net_tx_bytes":      cached.NetTxBytes,
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

// OperationStream is an SSE endpoint that streams real-time operation state
// for a single gameserver. Used by the UI detail page for live progress during
// downloads and other operations. Only active watchers receive updates —
// no load on the event bus.
func (h *GameserverHandlers) OperationStream(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, unwatch := h.svc.WatchOperation(id)
	defer unwatch()

	// Send the current state immediately so the client doesn't start blank
	current := h.svc.GetOperationState(id)
	data, _ := json.Marshal(current)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	ctx := r.Context()
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case op, ok := <-ch:
			if !ok {
				return
			}
			data, _ := json.Marshal(op)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func (h *GameserverHandlers) Logs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	tail := PaginationDefaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	// Specific historical session requested
	if v := r.URL.Query().Get("session"); v != "" {
		session, err := strconv.Atoi(v)
		if err != nil || session < 0 {
			respondError(w, http.StatusBadRequest, "invalid session number")
			return
		}
		lines, err := h.consoleSvc.ReadHistoricalLogs(r.Context(), id, session, tail)
		if err != nil {
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}
		if lines == nil {
			lines = []string{}
		}
		respondOK(w, map[string]any{"lines": lines, "historical": true, "session": session})
		return
	}

	reader, err := h.svc.GetInstanceLogs(r.Context(), id, tail)
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

	lines := logparse.ParseLogLines(reader)
	respondOK(w, map[string]any{"lines": lines})
}

func (h *GameserverHandlers) LogSessions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sessions, err := h.consoleSvc.ListLogSessions(r.Context(), id)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if sessions == nil {
		sessions = []gameserver.LogSession{}
	}
	respondOK(w, sessions)
}

func (h *GameserverHandlers) StreamLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	id := chi.URLParam(r, "id")
	tail := PaginationDefaultLogTail
	if v := r.URL.Query().Get("tail"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			tail = n
		}
	}

	reader, err := h.consoleSvc.StreamLogs(r.Context(), id, tail)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	lines := make(chan string, 64)
	go logparse.ParseLogStream(reader, lines)

	for {
		select {
		case <-r.Context().Done():
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", line)
			flusher.Flush()
		}
	}
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
		h.log.Error("sending command", "gameserver", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, map[string]string{"output": output})
}

func (h *GameserverHandlers) StatsHistory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	periodStr := r.URL.Query().Get("period")
	if periodStr == "" {
		periodStr = "1h"
	}
	period, ok := model.ValidStatsPeriod(periodStr)
	if !ok {
		respondError(w, http.StatusBadRequest, "invalid period: must be 1h, 24h, or 7d")
		return
	}

	samples, err := h.statsHistory.QueryHistory(id, period)
	if err != nil {
		h.log.Error("querying stats history", "gameserver", id, "period", period, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	// Compute net I/O rates (bytes/sec) from cumulative counters
	type statsPoint struct {
		Timestamp        time.Time `json:"timestamp"`
		CPUPercent       float64   `json:"cpu_percent"`
		MemoryUsageMB    int       `json:"memory_usage_mb"`
		MemoryLimitMB    int       `json:"memory_limit_mb"`
		NetRxBytesPerSec float64   `json:"net_rx_bytes_per_sec"`
		NetTxBytesPerSec float64   `json:"net_tx_bytes_per_sec"`
		VolumeSizeBytes  int64     `json:"volume_size_bytes"`
		PlayersOnline    int       `json:"players_online"`
	}

	points := make([]statsPoint, len(samples))
	for i, s := range samples {
		points[i] = statsPoint{
			Timestamp:       s.Timestamp,
			CPUPercent:      s.CPUPercent,
			MemoryUsageMB:   s.MemoryUsageMB,
			MemoryLimitMB:   s.MemoryLimitMB,
			VolumeSizeBytes: s.VolumeSizeBytes,
			PlayersOnline:   s.PlayersOnline,
		}
		if i > 0 {
			prev := samples[i-1]
			dt := s.Timestamp.Sub(prev.Timestamp).Seconds()
			if dt > 0 && s.NetRxBytes >= prev.NetRxBytes {
				points[i].NetRxBytesPerSec = float64(s.NetRxBytes-prev.NetRxBytes) / dt
				points[i].NetTxBytesPerSec = float64(s.NetTxBytes-prev.NetTxBytes) / dt
			}
		}
	}

	respondOK(w, points)
}
