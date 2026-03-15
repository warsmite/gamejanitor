package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type AuthHandlers struct {
	authSvc *service.AuthService
	log     *slog.Logger
}

func NewAuthHandlers(authSvc *service.AuthService, log *slog.Logger) *AuthHandlers {
	return &AuthHandlers{authSvc: authSvc, log: log}
}

func (h *AuthHandlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.authSvc.ListTokens()
	if err != nil {
		h.log.Error("listing tokens", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if tokens == nil {
		tokens = []models.Token{}
	}
	respondOK(w, tokens)
}

func (h *AuthHandlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string   `json:"name"`
		GameserverIDs []string `json:"gameserver_ids"`
		Permissions   []string `json:"permissions"`
		ExpiresIn     string   `json:"expires_in"` // e.g. "720h" for 30 days, empty = never
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			respondError(w, http.StatusBadRequest, "invalid expires_in duration: "+err.Error())
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	rawToken, token, err := h.authSvc.CreateScopedToken(req.Name, req.GameserverIDs, req.Permissions, expiresAt)
	if err != nil {
		h.log.Error("creating token", "error", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondCreated(w, map[string]any{
		"token":    rawToken,
		"token_id": token.ID,
		"name":     token.Name,
	})
}

func (h *AuthHandlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenId")
	if err := h.authSvc.DeleteToken(id); err != nil {
		h.log.Error("deleting token", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}
	respondNoContent(w)
}
