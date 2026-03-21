package handlers

import (
	"fmt"
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
	settingsSvc    *service.SettingsService
	workerNodeSvc  *service.WorkerNodeService
	authSvc        *service.AuthService
	webhookSender  *service.WebhookSender
	registry       *worker.Registry
	renderer       *Renderer
	dataDir        string
	log            *slog.Logger
}

func NewPageSettingsHandlers(settingsSvc *service.SettingsService, workerNodeSvc *service.WorkerNodeService, authSvc *service.AuthService, webhookSender *service.WebhookSender, registry *worker.Registry, renderer *Renderer, dataDir string, log *slog.Logger) *PageSettingsHandlers {
	return &PageSettingsHandlers{settingsSvc: settingsSvc, workerNodeSvc: workerNodeSvc, authSvc: authSvc, webhookSender: webhookSender, registry: registry, renderer: renderer, dataDir: dataDir, log: log}
}

func (h *PageSettingsHandlers) SettingsPage(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"DataDir":                h.dataDir,
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
		"AuditRetentionDays":       h.settingsSvc.GetAuditRetentionDays(),
		"AuditRetentionFromEnv":    h.settingsSvc.IsAuditRetentionFromEnv(),
		"RateLimitEnabled":         h.settingsSvc.GetRateLimitEnabled(),
		"RateLimitEnabledFromEnv":  h.settingsSvc.IsRateLimitEnabledFromEnv(),
		"RateLimitPerIP":           h.settingsSvc.GetRateLimitPerIP(),
		"RateLimitPerIPFromEnv":    h.settingsSvc.IsRateLimitPerIPFromEnv(),
		"RateLimitPerToken":        h.settingsSvc.GetRateLimitPerToken(),
		"RateLimitPerTokenFromEnv": h.settingsSvc.IsRateLimitPerTokenFromEnv(),
		"RateLimitLogin":           h.settingsSvc.GetRateLimitLogin(),
		"RateLimitLoginFromEnv":    h.settingsSvc.IsRateLimitLoginFromEnv(),
		"TrustProxyHeaders":        h.settingsSvc.GetTrustProxyHeaders(),
		"TrustProxyHeadersFromEnv": h.settingsSvc.IsTrustProxyHeadersFromEnv(),
		"WebhookEnabled":           h.settingsSvc.GetWebhookEnabled(),
		"WebhookEnabledFromEnv":    h.settingsSvc.IsWebhookEnabledFromEnv(),
		"WebhookURL":               h.settingsSvc.GetWebhookURL(),
		"WebhookURLFromEnv":        h.settingsSvc.IsWebhookURLFromEnv(),
		"WebhookSecretSet":         h.settingsSvc.GetWebhookSecret() != "",
		"WebhookSecretFromEnv":     h.settingsSvc.IsWebhookSecretFromEnv(),
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
	w.Header().Set("HX-Push-Url", "false")
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
	MaxMemoryMB       *int
	MaxCPU            *float64
	MaxStorageMB      *int
	AllocatedMemoryMB int
	AllocatedCPU      float64
	GameserverCount   int
	Cordoned          bool
}

func (h *PageSettingsHandlers) workerViews() []workerView {
	infos := h.registry.ListWorkers()
	defaultStart := h.settingsSvc.GetPortRangeStart()
	defaultEnd := h.settingsSvc.GetPortRangeEnd()

	// Count gameservers and allocated memory per node
	gsCountByNode := make(map[string]int)
	memByNode := make(map[string]int)
	cpuByNode := make(map[string]float64)
	if gameservers, err := h.workerNodeSvc.ListGameserversByNode(); err == nil {
		for _, gs := range gameservers {
			if gs.NodeID != nil && *gs.NodeID != "" {
				gsCountByNode[*gs.NodeID]++
				memByNode[*gs.NodeID] += gs.MemoryLimitMB
				cpuByNode[*gs.NodeID] += gs.CPULimit
			}
		}
	}

	views := make([]workerView, 0, len(infos))
	for _, info := range infos {
		age := time.Since(info.LastSeen)
		v := workerView{
			WorkerInfo:        info,
			IsHealthy:         age < 15*time.Second,
			IsWarning:         age >= 15*time.Second && age < 25*time.Second,
			DefaultRangeStart: defaultStart,
			DefaultRangeEnd:   defaultEnd,
			GameserverCount:   gsCountByNode[info.ID],
			AllocatedMemoryMB: memByNode[info.ID],
			AllocatedCPU:      cpuByNode[info.ID],
		}
		if node, err := h.workerNodeSvc.GetWorkerNode(info.ID); err == nil && node != nil {
			v.PortRangeStart = node.PortRangeStart
			v.PortRangeEnd = node.PortRangeEnd
			v.MaxMemoryMB = node.MaxMemoryMB
			v.MaxCPU = node.MaxCPU
			v.MaxStorageMB = node.MaxStorageMB
			v.Cordoned = node.Cordoned
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

	if err := h.workerNodeSvc.SetWorkerNodePortRange(workerID, &start, &end); err != nil {
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

	if err := h.workerNodeSvc.SetWorkerNodePortRange(workerID, nil, nil); err != nil {
		h.log.Error("clearing worker port range", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to clear worker port range: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker port range cleared", "worker_id", workerID)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) SaveWorkerLimits(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	var maxMemoryMB *int
	var maxCPU *float64
	var maxStorageMB *int

	if v := r.FormValue("max_memory_mb"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "Invalid max memory value", http.StatusBadRequest)
			return
		}
		if n > 0 {
			maxMemoryMB = &n
		}
	}

	if v := r.FormValue("max_cpu"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 {
			http.Error(w, "Invalid max CPU value", http.StatusBadRequest)
			return
		}
		if n > 0 {
			maxCPU = &n
		}
	}

	if v := r.FormValue("max_storage_mb"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "Invalid max storage value", http.StatusBadRequest)
			return
		}
		if n > 0 {
			maxStorageMB = &n
		}
	}

	if err := h.workerNodeSvc.SetWorkerNodeLimits(workerID, maxMemoryMB, maxCPU, maxStorageMB); err != nil {
		h.log.Error("setting worker limits", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to save worker limits: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker limits updated", "worker_id", workerID, "max_memory_mb", maxMemoryMB, "max_cpu", maxCPU, "max_storage_mb", maxStorageMB)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

// ClearWorkerLimits removes resource limits for a worker, reverting to unlimited.
func (h *PageSettingsHandlers) ClearWorkerLimits(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeLimits(workerID, nil, nil, nil); err != nil {
		h.log.Error("clearing worker limits", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to clear worker limits: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker limits cleared", "worker_id", workerID)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) CordonWorker(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeCordoned(workerID, true); err != nil {
		h.log.Error("cordoning worker", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to cordon worker: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker cordoned", "worker_id", workerID)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) UncordonWorker(w http.ResponseWriter, r *http.Request) {
	workerID := chi.URLParam(r, "workerID")

	if err := h.workerNodeSvc.SetWorkerNodeCordoned(workerID, false); err != nil {
		h.log.Error("uncordoning worker", "worker_id", workerID, "error", err)
		http.Error(w, "Failed to uncordon worker: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("worker uncordoned", "worker_id", workerID)
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

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

// saveIntHandler generates an http.HandlerFunc that parses a form int, validates, and saves.
func (h *PageSettingsHandlers) saveIntHandler(envLocked func() bool, formField string, min int, setter func(int) error, envMsg, errMsg, logMsg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if envLocked() {
			http.Error(w, envMsg+" is controlled by environment variable", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form data", http.StatusBadRequest)
			return
		}
		v, err := strconv.Atoi(r.FormValue(formField))
		if err != nil || v < min {
			http.Error(w, errMsg, http.StatusBadRequest)
			return
		}
		if err := setter(v); err != nil {
			h.log.Error(logMsg, "error", err)
			http.Error(w, "Failed to save setting", http.StatusInternalServerError)
			return
		}
		h.log.Info(logMsg, "value", v)
		w.Header().Set("HX-Redirect", "/settings")
		w.WriteHeader(http.StatusOK)
	}
}

// boolToggleHandler generates an http.HandlerFunc that sets a boolean setting.
func (h *PageSettingsHandlers) boolToggleHandler(envLocked func() bool, setter func(bool) error, envMsg, logMsg string, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if envLocked() {
			http.Error(w, envMsg+" is controlled by environment variable", http.StatusBadRequest)
			return
		}
		if err := setter(enabled); err != nil {
			h.log.Error(logMsg, "error", err)
			http.Error(w, "Failed to save setting", http.StatusInternalServerError)
			return
		}
		h.log.Info(logMsg, "enabled", enabled)
		w.Header().Set("HX-Redirect", "/settings")
		w.WriteHeader(http.StatusOK)
	}
}

func (h *PageSettingsHandlers) SaveMaxBackups(w http.ResponseWriter, r *http.Request) {
	h.saveIntHandler(h.settingsSvc.IsMaxBackupsFromEnv, "max_backups", 0, h.settingsSvc.SetMaxBackups,
		"Max backups", "Invalid max backups value", "max backups updated").ServeHTTP(w, r)
}

func (h *PageSettingsHandlers) SaveAuditRetention(w http.ResponseWriter, r *http.Request) {
	h.saveIntHandler(h.settingsSvc.IsAuditRetentionFromEnv, "audit_retention_days", 0, h.settingsSvc.SetAuditRetentionDays,
		"Audit retention", "Invalid audit retention value", "audit retention updated").ServeHTTP(w, r)
}

func (h *PageSettingsHandlers) SetRateLimitEnabled(enabled bool) http.HandlerFunc {
	return h.boolToggleHandler(h.settingsSvc.IsRateLimitEnabledFromEnv, h.settingsSvc.SetRateLimitEnabled,
		"Rate limiting", "rate limiting updated", enabled)
}

func (h *PageSettingsHandlers) SaveRateLimitPerIP(w http.ResponseWriter, r *http.Request) {
	h.saveIntHandler(h.settingsSvc.IsRateLimitPerIPFromEnv, "rate_limit_per_ip", 1, h.settingsSvc.SetRateLimitPerIP,
		"Rate limit per IP", "Invalid rate limit value (must be >= 1)", "rate limit per ip updated").ServeHTTP(w, r)
}

func (h *PageSettingsHandlers) SaveRateLimitPerToken(w http.ResponseWriter, r *http.Request) {
	h.saveIntHandler(h.settingsSvc.IsRateLimitPerTokenFromEnv, "rate_limit_per_token", 1, h.settingsSvc.SetRateLimitPerToken,
		"Rate limit per token", "Invalid rate limit value (must be >= 1)", "rate limit per token updated").ServeHTTP(w, r)
}

func (h *PageSettingsHandlers) SaveRateLimitLogin(w http.ResponseWriter, r *http.Request) {
	h.saveIntHandler(h.settingsSvc.IsRateLimitLoginFromEnv, "rate_limit_login", 1, h.settingsSvc.SetRateLimitLogin,
		"Login rate limit", "Invalid rate limit value (must be >= 1)", "rate limit login updated").ServeHTTP(w, r)
}

func (h *PageSettingsHandlers) SetTrustProxyHeaders(enabled bool) http.HandlerFunc {
	return h.boolToggleHandler(h.settingsSvc.IsTrustProxyHeadersFromEnv, h.settingsSvc.SetTrustProxyHeaders,
		"Trust proxy headers", "trust proxy headers updated", enabled)
}

func (h *PageSettingsHandlers) SetWebhookEnabled(enabled bool) http.HandlerFunc {
	return h.boolToggleHandler(h.settingsSvc.IsWebhookEnabledFromEnv, h.settingsSvc.SetWebhookEnabled,
		"Webhook enabled", "webhook enabled updated", enabled)
}

func (h *PageSettingsHandlers) SaveWebhookURL(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsWebhookURLFromEnv() {
		http.Error(w, "Webhook URL is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	url := strings.TrimSpace(r.FormValue("webhook_url"))
	if url == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetWebhookURL(url); err != nil {
		h.log.Error("setting webhook url", "error", err)
		http.Error(w, "Failed to save webhook URL", http.StatusInternalServerError)
		return
	}

	h.log.Info("webhook URL updated")
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) ClearWebhookURL(w http.ResponseWriter, r *http.Request) {
	if err := h.settingsSvc.ClearWebhookURL(); err != nil {
		h.log.Error("clearing webhook url", "error", err)
		http.Error(w, "Failed to clear webhook URL", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) SaveWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsWebhookSecretFromEnv() {
		http.Error(w, "Webhook secret is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	secret := strings.TrimSpace(r.FormValue("webhook_secret"))
	if secret == "" {
		http.Error(w, "Secret is required", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetWebhookSecret(secret); err != nil {
		h.log.Error("setting webhook secret", "error", err)
		http.Error(w, "Failed to save webhook secret", http.StatusInternalServerError)
		return
	}

	h.log.Info("webhook secret updated")
	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) ClearWebhookSecret(w http.ResponseWriter, r *http.Request) {
	if err := h.settingsSvc.ClearWebhookSecret(); err != nil {
		h.log.Error("clearing webhook secret", "error", err)
		http.Error(w, "Failed to clear webhook secret", http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/settings")
	w.WriteHeader(http.StatusOK)
}

func (h *PageSettingsHandlers) TestWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhookSender == nil {
		http.Error(w, "Webhooks not configured", http.StatusBadRequest)
		return
	}

	statusCode, err := h.webhookSender.SendTest()
	if err != nil {
		http.Error(w, "Webhook test failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	if statusCode >= 200 && statusCode < 300 {
		w.Header().Set("HX-Redirect", "/settings")
		w.WriteHeader(http.StatusOK)
	} else {
		http.Error(w, fmt.Sprintf("Webhook returned status %d", statusCode), http.StatusBadRequest)
	}
}

func (h *PageSettingsHandlers) SetLocalhostBypass(enabled bool) http.HandlerFunc {
	return h.boolToggleHandler(h.settingsSvc.IsLocalhostBypassFromEnv, h.settingsSvc.SetLocalhostBypass,
		"Localhost bypass", "localhost bypass updated", enabled)
}
