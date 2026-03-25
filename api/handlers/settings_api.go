package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/service"
)

type SettingsAPIHandlers struct {
	settingsSvc *service.SettingsService
	log         *slog.Logger
}

func NewSettingsAPIHandlers(settingsSvc *service.SettingsService, log *slog.Logger) *SettingsAPIHandlers {
	return &SettingsAPIHandlers{settingsSvc: settingsSvc, log: log}
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
