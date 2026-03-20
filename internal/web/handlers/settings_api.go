package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type SettingsAPIHandlers struct {
	settingsSvc *service.SettingsService
	log         *slog.Logger
}

func NewSettingsAPIHandlers(settingsSvc *service.SettingsService, log *slog.Logger) *SettingsAPIHandlers {
	return &SettingsAPIHandlers{settingsSvc: settingsSvc, log: log}
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
	LocalhostBypass            bool   `json:"localhost_bypass"`
	LocalhostBypassFromEnv     bool   `json:"localhost_bypass_from_env"`
	RateLimitEnabled           bool   `json:"rate_limit_enabled"`
	RateLimitEnabledFromEnv    bool   `json:"rate_limit_enabled_from_env"`
	RateLimitPerIP             int    `json:"rate_limit_per_ip"`
	RateLimitPerIPFromEnv      bool   `json:"rate_limit_per_ip_from_env"`
	RateLimitPerToken          int    `json:"rate_limit_per_token"`
	RateLimitPerTokenFromEnv   bool   `json:"rate_limit_per_token_from_env"`
	RateLimitLogin             int    `json:"rate_limit_login"`
	RateLimitLoginFromEnv      bool   `json:"rate_limit_login_from_env"`
	TrustProxyHeaders          bool   `json:"trust_proxy_headers"`
	TrustProxyHeadersFromEnv   bool   `json:"trust_proxy_headers_from_env"`
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
		LocalhostBypass:            h.settingsSvc.GetLocalhostBypass(),
		LocalhostBypassFromEnv:     h.settingsSvc.IsLocalhostBypassFromEnv(),
		RateLimitEnabled:           h.settingsSvc.GetRateLimitEnabled(),
		RateLimitEnabledFromEnv:    h.settingsSvc.IsRateLimitEnabledFromEnv(),
		RateLimitPerIP:             h.settingsSvc.GetRateLimitPerIP(),
		RateLimitPerIPFromEnv:      h.settingsSvc.IsRateLimitPerIPFromEnv(),
		RateLimitPerToken:          h.settingsSvc.GetRateLimitPerToken(),
		RateLimitPerTokenFromEnv:   h.settingsSvc.IsRateLimitPerTokenFromEnv(),
		RateLimitLogin:             h.settingsSvc.GetRateLimitLogin(),
		RateLimitLoginFromEnv:      h.settingsSvc.IsRateLimitLoginFromEnv(),
		TrustProxyHeaders:          h.settingsSvc.GetTrustProxyHeaders(),
		TrustProxyHeadersFromEnv:   h.settingsSvc.IsTrustProxyHeadersFromEnv(),
	})
}

func (h *SettingsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	for key, raw := range req {
		switch key {
		case "connection_address":
			if h.settingsSvc.IsConnectionAddressFromEnv() {
				respondError(w, http.StatusBadRequest, "connection_address is controlled by environment variable")
				return
			}
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid connection_address value")
				return
			}
			if v == "" {
				if err := h.settingsSvc.ClearConnectionAddress(); err != nil {
					respondError(w, http.StatusInternalServerError, err.Error())
					return
				}
			} else {
				if err := h.settingsSvc.SetConnectionAddress(v); err != nil {
					respondError(w, http.StatusInternalServerError, err.Error())
					return
				}
			}

		case "port_range_start":
			if h.settingsSvc.IsPortRangeFromEnv() {
				respondError(w, http.StatusBadRequest, "port_range is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 1024 || v > 65535 {
				respondError(w, http.StatusBadRequest, "invalid port_range_start (1024-65535)")
				return
			}
			if err := h.settingsSvc.SetPortRangeStart(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "port_range_end":
			if h.settingsSvc.IsPortRangeFromEnv() {
				respondError(w, http.StatusBadRequest, "port_range is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 1024 || v > 65535 {
				respondError(w, http.StatusBadRequest, "invalid port_range_end (1024-65535)")
				return
			}
			if err := h.settingsSvc.SetPortRangeEnd(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "port_mode":
			if h.settingsSvc.IsPortModeFromEnv() {
				respondError(w, http.StatusBadRequest, "port_mode is controlled by environment variable")
				return
			}
			var v string
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid port_mode value")
				return
			}
			if err := h.settingsSvc.SetPreferredPortMode(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "max_backups":
			if h.settingsSvc.IsMaxBackupsFromEnv() {
				respondError(w, http.StatusBadRequest, "max_backups is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
				respondError(w, http.StatusBadRequest, "invalid max_backups value")
				return
			}
			if err := h.settingsSvc.SetMaxBackups(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "auth_enabled":
			if h.settingsSvc.IsAuthEnabledFromEnv() {
				respondError(w, http.StatusBadRequest, "auth_enabled is controlled by environment variable")
				return
			}
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid auth_enabled value")
				return
			}
			if err := h.settingsSvc.SetAuthEnabled(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "localhost_bypass":
			if h.settingsSvc.IsLocalhostBypassFromEnv() {
				respondError(w, http.StatusBadRequest, "localhost_bypass is controlled by environment variable")
				return
			}
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid localhost_bypass value")
				return
			}
			if err := h.settingsSvc.SetLocalhostBypass(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "rate_limit_enabled":
			if h.settingsSvc.IsRateLimitEnabledFromEnv() {
				respondError(w, http.StatusBadRequest, "rate_limit_enabled is controlled by environment variable")
				return
			}
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid rate_limit_enabled value")
				return
			}
			if err := h.settingsSvc.SetRateLimitEnabled(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "rate_limit_per_ip":
			if h.settingsSvc.IsRateLimitPerIPFromEnv() {
				respondError(w, http.StatusBadRequest, "rate_limit_per_ip is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 1 {
				respondError(w, http.StatusBadRequest, "invalid rate_limit_per_ip value (must be >= 1)")
				return
			}
			if err := h.settingsSvc.SetRateLimitPerIP(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "rate_limit_per_token":
			if h.settingsSvc.IsRateLimitPerTokenFromEnv() {
				respondError(w, http.StatusBadRequest, "rate_limit_per_token is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 1 {
				respondError(w, http.StatusBadRequest, "invalid rate_limit_per_token value (must be >= 1)")
				return
			}
			if err := h.settingsSvc.SetRateLimitPerToken(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "rate_limit_login":
			if h.settingsSvc.IsRateLimitLoginFromEnv() {
				respondError(w, http.StatusBadRequest, "rate_limit_login is controlled by environment variable")
				return
			}
			var v int
			if err := json.Unmarshal(raw, &v); err != nil || v < 1 {
				respondError(w, http.StatusBadRequest, "invalid rate_limit_login value (must be >= 1)")
				return
			}
			if err := h.settingsSvc.SetRateLimitLogin(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		case "trust_proxy_headers":
			if h.settingsSvc.IsTrustProxyHeadersFromEnv() {
				respondError(w, http.StatusBadRequest, "trust_proxy_headers is controlled by environment variable")
				return
			}
			var v bool
			if err := json.Unmarshal(raw, &v); err != nil {
				respondError(w, http.StatusBadRequest, "invalid trust_proxy_headers value")
				return
			}
			if err := h.settingsSvc.SetTrustProxyHeaders(v); err != nil {
				respondError(w, http.StatusInternalServerError, err.Error())
				return
			}

		default:
			respondError(w, http.StatusBadRequest, "unknown setting: "+key)
			return
		}
	}

	h.log.Info("settings updated via API", "fields", len(req))

	// Return current state after update
	h.Get(w, r)
}
