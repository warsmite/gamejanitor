package handler

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/settings"
	"encoding/json"
	"log/slog"
	"net/http"
)

type SettingsAPIHandlers struct {
	settingsSvc *settings.SettingsService
	authSvc     *auth.AuthService
	log         *slog.Logger
}

func NewSettingsAPIHandlers(settingsSvc *settings.SettingsService, authSvc *auth.AuthService, log *slog.Logger) *SettingsAPIHandlers {
	return &SettingsAPIHandlers{settingsSvc: settingsSvc, authSvc: authSvc, log: log}
}

func (h *SettingsAPIHandlers) Get(w http.ResponseWriter, r *http.Request) {
	respondOK(w, h.settingsSvc.All())
}

func (h *SettingsAPIHandlers) Update(w http.ResponseWriter, r *http.Request) {
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	// Guard: don't allow enabling auth without at least one admin token
	if raw, ok := req[settings.SettingAuthEnabled]; ok {
		var enabling bool
		if err := json.Unmarshal(raw, &enabling); err == nil && enabling {
			tokens, _ := h.authSvc.ListTokensByRole("admin")
			if len(tokens) == 0 {
				respondError(w, http.StatusBadRequest, "cannot enable auth: no admin tokens exist. Create one first with: gamejanitor tokens offline create --name admin --type admin")
				return
			}
		}
	}

	// Guard: don't allow disabling localhost bypass without auth enabled + admin token
	if raw, ok := req[settings.SettingLocalhostBypass]; ok {
		var disabling bool
		if err := json.Unmarshal(raw, &disabling); err == nil && !disabling {
			if !h.settingsSvc.GetBool(settings.SettingAuthEnabled) {
				respondError(w, http.StatusBadRequest, "cannot disable localhost bypass: auth is not enabled. Enable auth first.")
				return
			}
		}
	}

	for key, raw := range req {
		if !h.settingsSvc.IsKnown(key) {
			respondError(w, http.StatusBadRequest, "unknown setting: "+key)
			return
		}

		// Unmarshal JSON into a generic value — json.Unmarshal produces
		// bool, float64, string, which Set()/coerce() handles.
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			respondError(w, http.StatusBadRequest, "invalid value for "+key)
			return
		}

		// Empty string clears the setting (reverts to default)
		if str, ok := value.(string); ok && str == "" {
			if err := h.settingsSvc.Clear(key); err != nil {
				respondError(w, http.StatusInternalServerError, "failed to clear setting")
				return
			}
			continue
		}

		if err := h.settingsSvc.Set(key, value); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	h.log.Info("settings updated via API", "fields", len(req))

	// Return current state after update
	h.Get(w, r)
}
