package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/go-chi/chi/v5"
)

type ScheduleHandlers struct {
	svc *service.ScheduleService
	log *slog.Logger
}

func NewScheduleHandlers(svc *service.ScheduleService, log *slog.Logger) *ScheduleHandlers {
	return &ScheduleHandlers{svc: svc, log: log}
}

func (h *ScheduleHandlers) List(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	schedules, err := h.svc.ListSchedules(gsID)
	if err != nil {
		h.log.Error("listing schedules", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if schedules == nil {
		schedules = []models.Schedule{}
	}
	respondOK(w, schedules)
}

func (h *ScheduleHandlers) Get(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	scheduleID := chi.URLParam(r, "scheduleId")
	schedule, err := h.svc.GetSchedule(gsID, scheduleID)
	if err != nil {
		h.log.Error("getting schedule", "id", scheduleID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, schedule)
}

func (h *ScheduleHandlers) Create(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	var req struct {
		Name     string          `json:"name"`
		Type     string          `json:"type"`
		CronExpr string          `json:"cron_expr"`
		Payload  json.RawMessage `json:"payload"`
		Enabled  *bool           `json:"enabled"`
		OneShot  bool            `json:"one_shot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Name == "" || req.Type == "" || req.CronExpr == "" {
		respondError(w, http.StatusBadRequest, "name, type, and cron_expr are required")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	payload := req.Payload
	if payload == nil {
		payload = json.RawMessage("{}")
	}

	schedule := &models.Schedule{
		GameserverID: gsID,
		Name:         req.Name,
		Type:         req.Type,
		CronExpr:     req.CronExpr,
		Payload:      payload,
		Enabled:      enabled,
		OneShot:      req.OneShot,
	}

	if err := h.svc.CreateSchedule(r.Context(), schedule); err != nil {
		h.log.Error("creating schedule", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondCreated(w, schedule)
}

func (h *ScheduleHandlers) Update(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	id := chi.URLParam(r, "scheduleId")

	existing, err := h.svc.GetSchedule(gsID, id)
	if err != nil {
		h.log.Error("getting schedule for update", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	var req struct {
		Name     *string          `json:"name"`
		Type     *string          `json:"type"`
		CronExpr *string          `json:"cron_expr"`
		Payload  *json.RawMessage `json:"payload"`
		Enabled  *bool            `json:"enabled"`
		OneShot  *bool            `json:"one_shot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Name != nil {
		existing.Name = *req.Name
	}
	if req.Type != nil {
		existing.Type = *req.Type
	}
	if req.CronExpr != nil {
		existing.CronExpr = *req.CronExpr
	}
	if req.Payload != nil {
		existing.Payload = *req.Payload
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}
	if req.OneShot != nil {
		existing.OneShot = *req.OneShot
	}

	if err := h.svc.UpdateSchedule(r.Context(), existing); err != nil {
		h.log.Error("updating schedule", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, existing)
}

func (h *ScheduleHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	id := chi.URLParam(r, "scheduleId")
	if err := h.svc.DeleteSchedule(r.Context(), gsID, id); err != nil {
		h.log.Error("deleting schedule", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}
