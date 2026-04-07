package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/go-chi/chi/v5"
)

type WebhookHandlers struct {
	svc *webhook.WebhookEndpointService
	log *slog.Logger
}

func NewWebhookHandlers(svc *webhook.WebhookEndpointService, log *slog.Logger) *WebhookHandlers {
	return &WebhookHandlers{svc: svc, log: log}
}

func (h *WebhookHandlers) List(w http.ResponseWriter, r *http.Request) {
	views, err := h.svc.List()
	if err != nil {
		h.log.Error("listing webhooks", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WebhookHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "webhookId")
	view, err := h.svc.Get(id)
	if err != nil {
		h.log.Error("getting webhook", "id", id, "error", err)
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
		h.log.Error("creating webhook", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondCreated(w, result)
}

func (h *WebhookHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "webhookId")
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

	view, err := h.svc.Update(id, req.Description, req.URL, req.Secret, req.Events, req.Enabled)
	if err != nil {
		h.log.Error("updating webhook", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, view)
}

func (h *WebhookHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "webhookId")
	if err := h.svc.Delete(id); err != nil {
		h.log.Error("deleting webhook", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}

func (h *WebhookHandlers) Deliveries(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "webhookId")
	p := parsePagination(r)
	views, err := h.svc.ListDeliveries(id, r.URL.Query().Get("state"), p.Limit)
	if err != nil {
		h.log.Error("listing webhook deliveries", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, views)
}

func (h *WebhookHandlers) Test(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "webhookId")
	result, err := h.svc.Test(id)
	if err != nil {
		h.log.Error("testing webhook", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, result)
}
