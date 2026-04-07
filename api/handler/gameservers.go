package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/controller/console"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/lifecycle"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/model"
	"github.com/go-chi/chi/v5"
	"strings"
)

// StatsHistoryQuerier reads historical stats from the database.
type StatsHistoryQuerier interface {
	QueryHistory(gameserverID string, period model.StatsPeriod) ([]model.StatsSample, error)
}

type GameserverHandlers struct {
	svc          *gameserver.GameserverService
	lifecycle    *lifecycle.Service
	consoleSvc   *console.Service
	querySvc     *status.QueryService
	statsPoller  *status.StatsPoller
	statsHistory StatsHistoryQuerier
	ops          *gameserver.Runner
	tracker      *gameserver.Tracker
	log          *slog.Logger
}

func NewGameserverHandlers(svc *gameserver.GameserverService, lifecycleSvc *lifecycle.Service, consoleSvc *console.Service, querySvc *status.QueryService, statsPoller *status.StatsPoller, statsHistory StatsHistoryQuerier, ops *gameserver.Runner, tracker *gameserver.Tracker, log *slog.Logger) *GameserverHandlers {
	return &GameserverHandlers{svc: svc, lifecycle: lifecycleSvc, consoleSvc: consoleSvc, querySvc: querySvc, statsPoller: statsPoller, statsHistory: statsHistory, ops: ops, tracker: tracker, log: log}
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
	respondOK(w, struct {
		SFTPPassword string `json:"sftp_password"`
	}{SFTPPassword: rawPassword})
}

func (h *GameserverHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var gs model.Gameserver
	if err := json.NewDecoder(r.Body).Decode(&gs); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	gs.ID = id

	if err := h.svc.UpdateGameserver(r.Context(), &gs); err != nil {
		h.log.Error("updating gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	// Re-read from DB to get final state with derived fields
	updated, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after update", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, updated)
}
