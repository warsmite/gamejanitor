package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/service"
	"github.com/go-chi/chi/v5"
)

type WebhookHandlers struct {
	svc *service.WebhookEndpointService
	log *slog.Logger
}

func NewWebhookHandlers(svc *service.WebhookEndpointService, log *slog.Logger) *WebhookHandlers {
	return &WebhookHandlers{svc: svc, log: log}
}

func (h *WebhookHandlers) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List()
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WebhookHandlers) Get(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.Get(chi.URLParam(r, "webhookId"))
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

func (h *WebhookHandlers) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string   `json:"description"`
		URL         string   `json:"url"`
		Secret      string   `json:"secret"`
		Events      []string `json:"events"`
		Enabled     *bool    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	result, err := h.svc.Create(req.URL, req.Description, req.Secret, req.Events, enabled)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondCreated(w, result)
}

func (h *WebhookHandlers) Update(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description *string  `json:"description"`
		URL         *string  `json:"url"`
		Secret      *string  `json:"secret"`
		Events      []string `json:"events"`
		Enabled     *bool    `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	view, err := h.svc.Update(chi.URLParam(r, "webhookId"), req.Description, req.URL, req.Secret, req.Events, req.Enabled)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

func (h *WebhookHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	if err := h.svc.Delete(chi.URLParam(r, "webhookId")); err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}

func (h *WebhookHandlers) Deliveries(w http.ResponseWriter, r *http.Request) {
	limit := constants.PaginationDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= constants.PaginationMaxLimit {
			limit = n
		}
	}

	views, err := h.svc.ListDeliveries(chi.URLParam(r, "webhookId"), r.URL.Query().Get("state"), limit)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WebhookHandlers) Test(w http.ResponseWriter, r *http.Request) {
	result, err := h.svc.Test(chi.URLParam(r, "webhookId"))
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, result)
}
