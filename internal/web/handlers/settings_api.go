package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type SettingsAPIHandlers struct {
	settingsSvc   *service.SettingsService
	webhookSender *service.WebhookSender
	log           *slog.Logger
}

func NewSettingsAPIHandlers(settingsSvc *service.SettingsService, webhookSender *service.WebhookSender, log *slog.Logger) *SettingsAPIHandlers {
	return &SettingsAPIHandlers{settingsSvc: settingsSvc, webhookSender: webhookSender, log: log}
}

type settingsResponse struct {
	ConnectionAddress        string `json:"connection_address"`
	ConnectionAddressFromEnv bool   `json:"connection_address_from_env"`
	PortRangeStart           int    `json:"port_range_start"`
	PortRangeEnd             int    `json:"port_range_end"`
	PortRangeFromEnv         bool   `json:"port_range_from_env"`
	PortMode                 string `json:"port_mode"`
	PortModeFromEnv          bool   `json:"port_mode_from_env"`
	MaxBackups               int    `json:"max_backups"`
	MaxBackupsFromEnv        bool   `json:"max_backups_from_env"`
	AuthEnabled              bool   `json:"auth_enabled"`
	AuthFromEnv              bool   `json:"auth_from_env"`
	LocalhostBypass          bool   `json:"localhost_bypass"`
	LocalhostBypassFromEnv   bool   `json:"localhost_bypass_from_env"`
	RateLimitEnabled         bool   `json:"rate_limit_enabled"`
	RateLimitEnabledFromEnv  bool   `json:"rate_limit_enabled_from_env"`
	RateLimitPerIP           int    `json:"rate_limit_per_ip"`
	RateLimitPerIPFromEnv    bool   `json:"rate_limit_per_ip_from_env"`
	RateLimitPerToken        int    `json:"rate_limit_per_token"`
	RateLimitPerTokenFromEnv bool   `json:"rate_limit_per_token_from_env"`
	RateLimitLogin           int    `json:"rate_limit_login"`
	RateLimitLoginFromEnv    bool   `json:"rate_limit_login_from_env"`
	TrustProxyHeaders        bool   `json:"trust_proxy_headers"`
	TrustProxyHeadersFromEnv bool   `json:"trust_proxy_headers_from_env"`
	WebhookEnabled           bool   `json:"webhook_enabled"`
	WebhookEnabledFromEnv    bool   `json:"webhook_enabled_from_env"`
	WebhookURL               string `json:"webhook_url"`
	WebhookURLFromEnv        bool   `json:"webhook_url_from_env"`
	WebhookSecretSet         bool   `json:"webhook_secret_set"`
	WebhookSecretFromEnv     bool   `json:"webhook_secret_from_env"`
}

func (h *SettingsAPIHandlers) Get(w http.ResponseWriter, r *http.Request) {
	respondOK(w, settingsResponse{
		ConnectionAddress:        h.settingsSvc.GetConnectionAddress(),
		ConnectionAddressFromEnv: h.settingsSvc.IsConnectionAddressFromEnv(),
		PortRangeStart:           h.settingsSvc.GetPortRangeStart(),
		PortRangeEnd:             h.settingsSvc.GetPortRangeEnd(),
		PortRangeFromEnv:         h.settingsSvc.IsPortRangeFromEnv(),
		PortMode:                 h.settingsSvc.GetPreferredPortMode(),
		PortModeFromEnv:          h.settingsSvc.IsPortModeFromEnv(),
		MaxBackups:               h.settingsSvc.GetMaxBackups(),
		MaxBackupsFromEnv:        h.settingsSvc.IsMaxBackupsFromEnv(),
		AuthEnabled:              h.settingsSvc.GetAuthEnabled(),
		AuthFromEnv:              h.settingsSvc.IsAuthEnabledFromEnv(),
		LocalhostBypass:          h.settingsSvc.GetLocalhostBypass(),
		LocalhostBypassFromEnv:   h.settingsSvc.IsLocalhostBypassFromEnv(),
		RateLimitEnabled:         h.settingsSvc.GetRateLimitEnabled(),
		RateLimitEnabledFromEnv:  h.settingsSvc.IsRateLimitEnabledFromEnv(),
		RateLimitPerIP:           h.settingsSvc.GetRateLimitPerIP(),
		RateLimitPerIPFromEnv:    h.settingsSvc.IsRateLimitPerIPFromEnv(),
		RateLimitPerToken:        h.settingsSvc.GetRateLimitPerToken(),
		RateLimitPerTokenFromEnv: h.settingsSvc.IsRateLimitPerTokenFromEnv(),
		RateLimitLogin:           h.settingsSvc.GetRateLimitLogin(),
		RateLimitLoginFromEnv:    h.settingsSvc.IsRateLimitLoginFromEnv(),
		TrustProxyHeaders:        h.settingsSvc.GetTrustProxyHeaders(),
		TrustProxyHeadersFromEnv: h.settingsSvc.IsTrustProxyHeadersFromEnv(),
		WebhookEnabled:           h.settingsSvc.GetWebhookEnabled(),
		WebhookEnabledFromEnv:    h.settingsSvc.IsWebhookEnabledFromEnv(),
		WebhookURL:               h.settingsSvc.GetWebhookURL(),
		WebhookURLFromEnv:        h.settingsSvc.IsWebhookURLFromEnv(),
		WebhookSecretSet:         h.settingsSvc.GetWebhookSecret() != "",
		WebhookSecretFromEnv:     h.settingsSvc.IsWebhookSecretFromEnv(),
	})
}

// badRequestError signals a 400 response in the settings update loop.
type badRequestError string

func (e badRequestError) Error() string { return string(e) }

type settingDef struct {
	envLocked func() bool
	apply     func(json.RawMessage) error // returns badRequestError for 400, other errors for 500
}

func (h *SettingsAPIHandlers) settingDefs() map[string]settingDef {
	svc := h.settingsSvc

	boolSetting := func(envLocked func() bool, key string, setter func(bool) error) settingDef {
		return settingDef{
			envLocked: envLocked,
			apply: func(raw json.RawMessage) error {
				var v bool
				if err := json.Unmarshal(raw, &v); err != nil {
					return badRequestError("invalid " + key + " value")
				}
				return setter(v)
			},
		}
	}

	intSetting := func(envLocked func() bool, key string, min, max int, errMsg string, setter func(int) error) settingDef {
		return settingDef{
			envLocked: envLocked,
			apply: func(raw json.RawMessage) error {
				var v int
				if err := json.Unmarshal(raw, &v); err != nil || v < min || (max > 0 && v > max) {
					return badRequestError(errMsg)
				}
				return setter(v)
			},
		}
	}

	stringSetting := func(envLocked func() bool, key string, setter func(string) error) settingDef {
		return settingDef{
			envLocked: envLocked,
			apply: func(raw json.RawMessage) error {
				var v string
				if err := json.Unmarshal(raw, &v); err != nil {
					return badRequestError("invalid " + key + " value")
				}
				return setter(v)
			},
		}
	}

	// Clearable string: empty string clears the setting, non-empty sets it.
	clearableStringSetting := func(envLocked func() bool, key string, setter func(string) error, clear func() error) settingDef {
		return settingDef{
			envLocked: envLocked,
			apply: func(raw json.RawMessage) error {
				var v string
				if err := json.Unmarshal(raw, &v); err != nil {
					return badRequestError("invalid " + key + " value")
				}
				if v == "" {
					return clear()
				}
				return setter(v)
			},
		}
	}

	return map[string]settingDef{
		"connection_address": clearableStringSetting(svc.IsConnectionAddressFromEnv, "connection_address", svc.SetConnectionAddress, svc.ClearConnectionAddress),
		"port_range_start":   intSetting(svc.IsPortRangeFromEnv, "port_range_start", 1024, 65535, "invalid port_range_start (1024-65535)", svc.SetPortRangeStart),
		"port_range_end":     intSetting(svc.IsPortRangeFromEnv, "port_range_end", 1024, 65535, "invalid port_range_end (1024-65535)", svc.SetPortRangeEnd),
		"port_mode":          stringSetting(svc.IsPortModeFromEnv, "port_mode", svc.SetPreferredPortMode),
		"max_backups":        intSetting(svc.IsMaxBackupsFromEnv, "max_backups", 0, 0, "invalid max_backups value", svc.SetMaxBackups),
		"auth_enabled":       boolSetting(svc.IsAuthEnabledFromEnv, "auth_enabled", svc.SetAuthEnabled),
		"localhost_bypass":   boolSetting(svc.IsLocalhostBypassFromEnv, "localhost_bypass", svc.SetLocalhostBypass),
		"rate_limit_enabled": boolSetting(svc.IsRateLimitEnabledFromEnv, "rate_limit_enabled", svc.SetRateLimitEnabled),
		"rate_limit_per_ip":    intSetting(svc.IsRateLimitPerIPFromEnv, "rate_limit_per_ip", 1, 0, "invalid rate_limit_per_ip value (must be >= 1)", svc.SetRateLimitPerIP),
		"rate_limit_per_token": intSetting(svc.IsRateLimitPerTokenFromEnv, "rate_limit_per_token", 1, 0, "invalid rate_limit_per_token value (must be >= 1)", svc.SetRateLimitPerToken),
		"rate_limit_login":     intSetting(svc.IsRateLimitLoginFromEnv, "rate_limit_login", 1, 0, "invalid rate_limit_login value (must be >= 1)", svc.SetRateLimitLogin),
		"trust_proxy_headers": boolSetting(svc.IsTrustProxyHeadersFromEnv, "trust_proxy_headers", svc.SetTrustProxyHeaders),
		"webhook_enabled":     boolSetting(svc.IsWebhookEnabledFromEnv, "webhook_enabled", svc.SetWebhookEnabled),
		"webhook_url":         clearableStringSetting(svc.IsWebhookURLFromEnv, "webhook_url", svc.SetWebhookURL, svc.ClearWebhookURL),
		"webhook_secret":      clearableStringSetting(svc.IsWebhookSecretFromEnv, "webhook_secret", svc.SetWebhookSecret, svc.ClearWebhookSecret),
	}
}

func (h *SettingsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	defs := h.settingDefs()

	for key, raw := range req {
		def, ok := defs[key]
		if !ok {
			respondError(w, http.StatusBadRequest, "unknown setting: "+key)
			return
		}
		if def.envLocked() {
			respondError(w, http.StatusBadRequest, key+" is controlled by environment variable")
			return
		}
		if err := def.apply(raw); err != nil {
			if _, isBadReq := err.(badRequestError); isBadReq {
				respondError(w, http.StatusBadRequest, err.Error())
			} else {
				respondError(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
	}

	h.log.Info("settings updated via API", "fields", len(req))

	// Return current state after update
	h.Get(w, r)
}

func (h *SettingsAPIHandlers) TestWebhook(w http.ResponseWriter, r *http.Request) {
	if h.webhookSender == nil {
		respondError(w, http.StatusBadRequest, "webhooks not configured")
		return
	}

	statusCode, err := h.webhookSender.SendTest()
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondOK(w, map[string]any{
		"response_status": statusCode,
		"success":         statusCode >= 200 && statusCode < 300,
	})
}
