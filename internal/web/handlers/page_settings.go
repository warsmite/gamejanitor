package handlers

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/csrf"
)

type PageSettingsHandlers struct {
	settingsSvc *service.SettingsService
	authSvc     *service.AuthService
	registry    *worker.Registry
	renderer    *Renderer
	log         *slog.Logger
}

func NewPageSettingsHandlers(settingsSvc *service.SettingsService, authSvc *service.AuthService, registry *worker.Registry, renderer *Renderer, log *slog.Logger) *PageSettingsHandlers {
	return &PageSettingsHandlers{settingsSvc: settingsSvc, authSvc: authSvc, registry: registry, renderer: renderer, log: log}
}

// SettingsPage renders the main settings page.
func (h *PageSettingsHandlers) SettingsPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"PortRangeStart":         h.settingsSvc.GetPortRangeStart(),
		"PortRangeEnd":           h.settingsSvc.GetPortRangeEnd(),
		"PortRangeFromEnv":       h.settingsSvc.IsPortRangeFromEnv(),
		"PreferredPortMode":      h.settingsSvc.GetPreferredPortMode(),
		"PortModeFromEnv":        h.settingsSvc.IsPortModeFromEnv(),
		"MaxBackups":             h.settingsSvc.GetMaxBackups(),
		"MaxBackupsFromEnv":      h.settingsSvc.IsMaxBackupsFromEnv(),
		"AuthEnabled":            h.settingsSvc.GetAuthEnabled(),
		"AuthFromEnv":            h.settingsSvc.IsAuthEnabledFromEnv(),
		"LocalhostBypass":        h.settingsSvc.GetLocalhostBypass(),
		"LocalhostBypassFromEnv": h.settingsSvc.IsLocalhostBypassFromEnv(),
	}

	if h.registry != nil {
		data["MultiNode"] = true
		data["Workers"] = h.workerViews()
		data["WorkerTokens"] = h.workerTokens()
	}

	h.renderer.Render(w, r, "settings/index", data)
}

// WorkersPartial renders just the workers table for htmx polling.
func (h *PageSettingsHandlers) WorkersPartial(w http.ResponseWriter, r *http.Request) {
	var views []workerView
	if h.registry != nil {
		views = h.workerViews()
	}
	h.renderer.RenderPartial(w, "settings/index", "workers_table", map[string]any{
		"Workers":   views,
		"CSRFToken": csrf.Token(r),
	})
}

type workerView struct {
	worker.WorkerInfo
	IsHealthy         bool
	IsWarning         bool
	PortRangeStart    *int
	PortRangeEnd      *int
	DefaultRangeStart int
	DefaultRangeEnd   int
}

func (h *PageSettingsHandlers) workerViews() []workerView {
	infos := h.registry.ListWorkers()
	defaultStart := h.settingsSvc.GetPortRangeStart()
	defaultEnd := h.settingsSvc.GetPortRangeEnd()
	views := make([]workerView, 0, len(infos))
	for _, info := range infos {
		age := time.Since(info.LastSeen)
		v := workerView{
			WorkerInfo:        info,
			IsHealthy:         age < 15*time.Second,
			IsWarning:         age >= 15*time.Second && age < 25*time.Second,
			DefaultRangeStart: defaultStart,
			DefaultRangeEnd:   defaultEnd,
		}
		if node, err := h.settingsSvc.GetWorkerNode(info.ID); err == nil && node != nil {
			v.PortRangeStart = node.PortRangeStart
			v.PortRangeEnd = node.PortRangeEnd
		}
		views = append(views, v)
	}
	return views
}

func (h *PageSettingsHandlers) workerTokens() []models.Token {
	tokens, err := h.authSvc.ListTokens()
	if err != nil {
		h.log.Error("listing tokens for settings", "error", err)
		return nil
	}
	var workerTokens []models.Token
	for _, t := range tokens {
		if t.Scope == service.ScopeWorker {
			workerTokens = append(workerTokens, t)
		}
	}
	return workerTokens
}

// CreateWorkerToken handles creating a worker auth token.
func (h *PageSettingsHandlers) CreateWorkerToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "Token name is required", http.StatusBadRequest)
		return
	}

	rawToken, _, err := h.authSvc.CreateWorkerToken(name)
	if err != nil {
		h.log.Error("creating worker token", "error", err)
		http.Error(w, "Failed to create worker token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.renderer.Render(w, r, "auth/token_created", map[string]any{
		"RawToken":    rawToken,
		"Name":        name,
		"IsWorker":    true,
	})
}

// DeleteWorkerToken handles deleting a worker auth token.
func (h *PageSettingsHandlers) DeleteWorkerToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenId")
	if err := h.authSvc.DeleteToken(id); err != nil {
		h.log.Error("deleting worker token", "id", id, "error", err)
		http.Error(w, "Failed to delete token: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.log.Info("worker token deleted", "id", id)
	w.Header().Set("HX-Push-Url", "false")
	h.SettingsPage(w, r)
}

func (h *PageSettingsHandlers) SetConnectionAddress(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	address := strings.TrimSpace(r.FormValue("connection_address"))
	if address == "" {
		http.Error(w, "Address is required", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetConnectionAddress(address); err != nil {
		h.log.Error("setting connection address", "error", err)
		http.Error(w, "Failed to save connection address", http.StatusInternalServerError)
		return
	}

	referer := r.Header.Get("HX-Current-URL")
	if referer == "" {
		referer = r.Header.Get("Referer")
	}
	if referer == "" {
		referer = "/"
	}
	w.Header().Set("HX-Redirect", referer)
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) ClearConnectionAddress(w http.ResponseWriter, r *http.Request) {
	if err := h.settingsSvc.ClearConnectionAddress(); err != nil {
		h.log.Error("clearing connection address", "error", err)
		http.Error(w, "Failed to clear connection address", http.StatusInternalServerError)
		return
	}

	referer := r.Header.Get("HX-Current-URL")
	if referer == "" {
		referer = r.Header.Get("Referer")
	}
	if referer == "" {
		referer = "/"
	}
	w.Header().Set("HX-Redirect", referer)
	w.WriteHeader(http.StatusOK)
}

// SaveWorkerPortRange sets a custom port range for a specific worker node.
func (h *PageSettingsHandlers) SaveWorkerPortRange(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	start, err := strconv.Atoi(r.FormValue("port_range_start"))
	if err != nil || start < 1024 || start > 65535 {
		http.Error(w, "Invalid start port", http.StatusBadRequest)
		return
	}
	end, err := strconv.Atoi(r.FormValue("port_range_end"))
	if err != nil || end < 1024 || end > 65535 {
		http.Error(w, "Invalid end port", http.StatusBadRequest)
		return
	}
	if end <= start {
		http.Error(w, "End port must be greater than start port", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetWorkerNodePortRange(workerID, &start, &end); err != nil {
		h.log.Error("setting worker port range", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to save worker port range: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker port range updated", "worker_id", workerID, "start", start, "end", end)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// ClearWorkerPortRange reverts a worker to the global default port range.
func (h *PageSettingsHandlers) ClearWorkerPortRange(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.settingsSvc.SetWorkerNodePortRange(workerID, nil, nil); err != nil {
		h.log.Error("clearing worker port range", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to clear worker port range: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker port range cleared", "worker_id", workerID)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// SavePortRange handles the port range form submission.
func (h *PageSettingsHandlers) SavePortRange(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsPortRangeFromEnv() {
		http.Error(w, "Port range is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	start, err := strconv.Atoi(r.FormValue("port_range_start"))
	if err != nil || start < 1024 || start > 65535 {
		http.Error(w, "Invalid start port", http.StatusBadRequest)
		return
	}
	end, err := strconv.Atoi(r.FormValue("port_range_end"))
	if err != nil || end < 1024 || end > 65535 {
		http.Error(w, "Invalid end port", http.StatusBadRequest)
		return
	}
	if end <= start {
		http.Error(w, "End port must be greater than start port", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetPortRangeStart(start); err != nil {
		h.log.Error("setting port range start", "error", err)
		http.Error(w, "Failed to save port range", http.StatusInternalServerError)
		return
	}
	if err := h.settingsSvc.SetPortRangeEnd(end); err != nil {
		h.log.Error("setting port range end", "error", err)
		http.Error(w, "Failed to save port range", http.StatusInternalServerError)
		return
	}

	h.log.Info("port range updated", "start", start, "end", end)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// SavePortMode handles the port allocation mode form submission.
func (h *PageSettingsHandlers) SavePortMode(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsPortModeFromEnv() {
		http.Error(w, "Port mode is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	mode := r.FormValue("port_mode")
	if err := h.settingsSvc.SetPreferredPortMode(mode); err != nil {
		h.log.Error("setting port mode", "error", err)
		http.Error(w, "Failed to save port mode", http.StatusInternalServerError)
		return
	}

	h.log.Info("port mode updated", "mode", mode)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// SaveMaxBackups handles the max backups form submission.
func (h *PageSettingsHandlers) SaveMaxBackups(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsMaxBackupsFromEnv() {
		http.Error(w, "Max backups is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	v, err := strconv.Atoi(r.FormValue("max_backups"))
	if err != nil || v < 0 {
		http.Error(w, "Invalid max backups value", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetMaxBackups(v); err != nil {
		h.log.Error("setting max backups", "error", err)
		http.Error(w, "Failed to save max backups", http.StatusInternalServerError)
		return
	}

	h.log.Info("max backups updated", "value", v)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// SetLocalhostBypass handles enabling/disabling localhost bypass.
func (h *PageSettingsHandlers) SetLocalhostBypass(enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.settingsSvc.IsLocalhostBypassFromEnv() {
			http.Error(w, "Localhost bypass is controlled by environment variable", http.StatusBadRequest)
			return
		}

		if err := h.settingsSvc.SetLocalhostBypass(enabled); err != nil {
			h.log.Error("setting localhost bypass", "error", err)
			http.Error(w, "Failed to save localhost bypass", http.StatusInternalServerError)
			return
		}

		h.log.Info("localhost bypass updated", "enabled", enabled)
		w.Header().Set("HX-Redirect", "/settings")
		w.WriteHeader(http.StatusOK)
	}
}
