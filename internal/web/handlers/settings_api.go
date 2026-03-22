package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/internal/service"
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
	TrustProxyHeaders            bool `json:"trust_proxy_headers"`
	TrustProxyHeadersFromEnv     bool `json:"trust_proxy_headers_from_env"`
	EventRetentionDays           int  `json:"event_retention_days"`
	EventRetentionFromEnv        bool `json:"event_retention_from_env"`
	RequireMemoryLimit           bool `json:"require_memory_limit"`
	RequireMemoryLimitFromEnv    bool `json:"require_memory_limit_from_env"`
	RequireCPULimit              bool `json:"require_cpu_limit"`
	RequireCPULimitFromEnv       bool `json:"require_cpu_limit_from_env"`
	RequireStorageLimit          bool `json:"require_storage_limit"`
	RequireStorageLimitFromEnv   bool `json:"require_storage_limit_from_env"`
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
		TrustProxyHeaders:          h.settingsSvc.GetTrustProxyHeaders(),
		TrustProxyHeadersFromEnv:   h.settingsSvc.IsTrustProxyHeadersFromEnv(),
		EventRetentionDays:         h.settingsSvc.GetEventRetentionDays(),
		EventRetentionFromEnv:      h.settingsSvc.IsEventRetentionFromEnv(),
		RequireMemoryLimit:         h.settingsSvc.GetRequireMemoryLimit(),
		RequireMemoryLimitFromEnv:  h.settingsSvc.IsRequireMemoryLimitFromEnv(),
		RequireCPULimit:            h.settingsSvc.GetRequireCPULimit(),
		RequireCPULimitFromEnv:     h.settingsSvc.IsRequireCPULimitFromEnv(),
		RequireStorageLimit:        h.settingsSvc.GetRequireStorageLimit(),
		RequireStorageLimitFromEnv: h.settingsSvc.IsRequireStorageLimitFromEnv(),
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
		"trust_proxy_headers":    boolSetting(svc.IsTrustProxyHeadersFromEnv, "trust_proxy_headers", svc.SetTrustProxyHeaders),
		"event_retention_days":   intSetting(svc.IsEventRetentionFromEnv, "event_retention_days", 1, 365, "invalid event_retention_days (1-365)", svc.SetEventRetentionDays),
		"require_memory_limit":   boolSetting(svc.IsRequireMemoryLimitFromEnv, "require_memory_limit", svc.SetRequireMemoryLimit),
		"require_cpu_limit":      boolSetting(svc.IsRequireCPULimitFromEnv, "require_cpu_limit", svc.SetRequireCPULimit),
		"require_storage_limit":  boolSetting(svc.IsRequireStorageLimitFromEnv, "require_storage_limit", svc.SetRequireStorageLimit),
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

