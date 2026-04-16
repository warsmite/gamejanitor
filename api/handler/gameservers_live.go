package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker/logparse"
	"github.com/go-chi/chi/v5"
)

type queryInfo struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

func (h *GameserverHandlers) Query(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	qd := h.querySvc.GetQueryData(id)
	if qd == nil {
		respondOK(w, queryInfo{})
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

type statsResponse struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsageMB   int     `json:"memory_usage_mb"`
	MemoryLimitMB   int     `json:"memory_limit_mb"`
	VolumeSizeBytes int64   `json:"volume_size_bytes"`
	StorageLimitMB  *int    `json:"storage_limit_mb,omitempty"`
}

func (h *GameserverHandlers) Stats(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// Serve from poller cache if available (instant, no runtime query)
	if cached := h.statsPoller.GetCachedStats(id); cached != nil {
		respondOK(w, statsResponse{
			CPUPercent:      cached.CPUPercent,
			MemoryUsageMB:   cached.MemoryUsageMB,
			MemoryLimitMB:   cached.MemoryLimitMB,
			VolumeSizeBytes: cached.VolumeSizeBytes,
			StorageLimitMB:  cached.StorageLimitMB,
		})
		return
	}

	// Fallback: live fetch (poller not running yet)
	stats, err := h.manager.GetGameserverStats(r.Context(), id)
	if err != nil {
		h.log.Warn("failed to get gameserver stats", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, statsResponse{
		CPUPercent:      stats.CPUPercent,
		MemoryUsageMB:   stats.MemoryUsageMB,
		MemoryLimitMB:   stats.MemoryLimitMB,
		VolumeSizeBytes: stats.VolumeSizeBytes,
		StorageLimitMB:  stats.StorageLimitMB,
	})
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

	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	ch, unwatch := gs.Watch()
	defer unwatch()

	// Send the current state immediately so the client doesn't start blank
	current := gs.GetOperation()
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

type logsResponse struct {
	Lines      []string `json:"lines"`
	Historical bool     `json:"historical,omitempty"`
	Session    *int     `json:"session,omitempty"`
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
		respondOK(w, logsResponse{Lines: lines, Historical: true, Session: &session})
		return
	}

	reader, err := h.manager.GetInstanceLogs(r.Context(), id, tail)
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
		respondOK(w, logsResponse{Lines: lines, Historical: true})
		return
	}
	defer reader.Close()

	lines := logparse.ParseLogLines(reader)
	respondOK(w, logsResponse{Lines: lines})
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

	respondOK(w, struct {
		Output string `json:"output"`
	}{Output: output})
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

	type statsPoint struct {
		Timestamp       time.Time `json:"timestamp"`
		CPUPercent      float64   `json:"cpu_percent"`
		MemoryUsageMB   int       `json:"memory_usage_mb"`
		MemoryLimitMB   int       `json:"memory_limit_mb"`
		VolumeSizeBytes int64     `json:"volume_size_bytes"`
		PlayersOnline   int       `json:"players_online"`
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
	}

	respondOK(w, points)
}
