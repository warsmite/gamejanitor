package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageScheduleHandlers struct {
	scheduleSvc   *service.ScheduleService
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageScheduleHandlers(scheduleSvc *service.ScheduleService, gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageScheduleHandlers {
	return &PageScheduleHandlers{scheduleSvc: scheduleSvc, gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageScheduleHandlers) List(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for schedules", "id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if gs == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for schedules", "game_id", gs.GameID, "error", err)
	}

	schedules, err := h.scheduleSvc.ListSchedules(id)
	if err != nil {
		h.log.Error("listing schedules", "gameserver_id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if schedules == nil {
		schedules = []models.Schedule{}
	}

	h.renderer.Render(w, r, "gameservers/schedules", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"Schedules":  schedules,
	})
}

func (h *PageScheduleHandlers) Create(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	schedType := r.FormValue("type")
	cronExpr := r.FormValue("cron_expr")
	command := r.FormValue("command")

	if name == "" || schedType == "" || cronExpr == "" {
		http.Error(w, "Name, type, and cron expression are required", http.StatusBadRequest)
		return
	}

	payload := json.RawMessage("{}")
	if schedType == "command" && command != "" {
		payloadBytes, _ := json.Marshal(map[string]string{"command": command})
		payload = payloadBytes
	}

	schedule := &models.Schedule{
		GameserverID: id,
		Name:         name,
		Type:         schedType,
		CronExpr:     cronExpr,
		Payload:      payload,
		Enabled:      true,
	}

	if err := h.scheduleSvc.CreateSchedule(schedule); err != nil {
		h.log.Error("creating schedule from web", "error", err)
		http.Error(w, "Failed to create schedule: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, id)
}

func (h *PageScheduleHandlers) Update(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	scheduleID := chi.URLParam(r, "scheduleId")

	existing, err := h.scheduleSvc.GetSchedule(scheduleID)
	if err != nil || existing == nil {
		http.Error(w, "Schedule not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	if name := r.FormValue("name"); name != "" {
		existing.Name = name
	}
	if t := r.FormValue("type"); t != "" {
		existing.Type = t
	}
	if expr := r.FormValue("cron_expr"); expr != "" {
		existing.CronExpr = expr
	}
	if existing.Type == "command" {
		if cmd := r.FormValue("command"); cmd != "" {
			payloadBytes, _ := json.Marshal(map[string]string{"command": cmd})
			existing.Payload = payloadBytes
		}
	}

	if err := h.scheduleSvc.UpdateSchedule(existing); err != nil {
		h.log.Error("updating schedule from web", "id", scheduleID, "error", err)
		http.Error(w, "Failed to update schedule: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("HX-Redirect", "/gameservers/"+gsID+"/schedules")
	http.Redirect(w, r, "/gameservers/"+gsID+"/schedules", http.StatusSeeOther)
}

func (h *PageScheduleHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	scheduleID := chi.URLParam(r, "scheduleId")

	if err := h.scheduleSvc.DeleteSchedule(scheduleID); err != nil {
		h.log.Error("deleting schedule from web", "id", scheduleID, "error", err)
		http.Error(w, "Failed to delete schedule: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, gsID)
}

func (h *PageScheduleHandlers) Toggle(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	scheduleID := chi.URLParam(r, "scheduleId")

	if err := h.scheduleSvc.ToggleSchedule(scheduleID); err != nil {
		h.log.Error("toggling schedule from web", "id", scheduleID, "error", err)
		http.Error(w, "Failed to toggle schedule: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, gsID)
}

func (h *PageScheduleHandlers) renderList(w http.ResponseWriter, r *http.Request, gsID string) {
	w.Header().Set("HX-Push-Url", "false")
	gs, err := h.gameserverSvc.GetGameserver(gsID)
	if err != nil {
		h.log.Error("getting gameserver for schedules", "id", gsID, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for schedules", "game_id", gs.GameID, "error", err)
	}

	schedules, err := h.scheduleSvc.ListSchedules(gsID)
	if err != nil {
		h.log.Error("listing schedules", "gameserver_id", gsID, "error", err)
		http.Error(w, "Failed to load schedules", http.StatusInternalServerError)
		return
	}
	if schedules == nil {
		schedules = []models.Schedule{}
	}

	h.renderer.Render(w, r, "gameservers/schedules", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"Schedules":  schedules,
	})
}
