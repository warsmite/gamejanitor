package handler

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/warsmite/gamejanitor/model"
	"github.com/go-chi/chi/v5"
)

type AuthHandlers struct {
	authSvc *auth.AuthService
	log     *slog.Logger
}

func NewAuthHandlers(authSvc *auth.AuthService, log *slog.Logger) *AuthHandlers {
	return &AuthHandlers{authSvc: authSvc, log: log}
}

func (h *AuthHandlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	scope := r.URL.Query().Get("scope")

	var tokens []model.Token
	var err error
	if scope != "" {
		tokens, err = h.authSvc.ListTokensByScope(scope)
	} else {
		tokens, err = h.authSvc.ListTokens()
	}
	if err != nil {
		h.log.Error("listing tokens", "scope", scope, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if tokens == nil {
		tokens = []model.Token{}
	}
	respondOK(w, tokens)
}

func (h *AuthHandlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name          string   `json:"name"`
		Scope         string   `json:"scope"`
		GameserverIDs []string `json:"gameserver_ids"`
		Permissions   []string `json:"permissions"`
		ExpiresIn     string   `json:"expires_in"` // e.g. "720h" for 30 days, empty = never
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Scope == "" {
		req.Scope = "custom"
	}
	if req.Scope != "admin" && req.Scope != "custom" && req.Scope != "worker" {
		respondError(w, http.StatusBadRequest, "scope must be \"admin\", \"custom\", or \"worker\"")
		return
	}

	if req.Scope == "worker" {
		rawToken, token, err := h.authSvc.CreateWorkerToken(req.Name)
		if err != nil {
			h.log.Error("creating worker token", "error", err)
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}
		if rawToken == "" {
			respondOK(w, map[string]any{
				"token_id": token.ID,
				"name":     token.Name,
				"exists":   true,
			})
			return
		}
		respondCreated(w, map[string]any{
			"token":    rawToken,
			"token_id": token.ID,
			"name":     token.Name,
		})
		return
	}

	if req.Scope == "admin" {
		rawToken, token, err := h.authSvc.CreateAdminToken(req.Name)
		if err != nil {
			h.log.Error("creating admin token", "error", err)
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}
		if rawToken == "" {
			respondOK(w, map[string]any{
				"token_id": token.ID,
				"name":     token.Name,
				"exists":   true,
			})
			return
		}
		respondCreated(w, map[string]any{
			"token":    rawToken,
			"token_id": token.ID,
			"name":     token.Name,
		})
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

	rawToken, token, err := h.authSvc.CreateCustomToken(req.Name, req.GameserverIDs, req.Permissions, expiresAt)
	if err != nil {
		h.log.Error("creating token", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
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
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}

func (h *AuthHandlers) RotateToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenId")
	token, err := h.authSvc.GetToken(id)
	if err != nil || token == nil {
		respondError(w, 404, "token not found")
		return
	}
	if token.Scope != "worker" {
		respondError(w, 400, "only worker tokens can be rotated")
		return
	}
	rawToken, newToken, err := h.authSvc.RotateWorkerToken(token.Name)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, map[string]any{"token": rawToken, "token_id": newToken.ID, "name": newToken.Name})
}

// GenerateClaimCode creates or regenerates an invite link claim code for a token.
func (h *AuthHandlers) GenerateClaimCode(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenId")
	code, err := h.authSvc.GenerateClaimCode(id)
	if err != nil {
		h.log.Error("generating claim code", "token", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, map[string]string{"claim_code": code})
}

// RedeemClaimCode is a public endpoint that exchanges a claim code for a raw token.
func (h *AuthHandlers) RedeemClaimCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Code == "" {
		respondError(w, http.StatusBadRequest, "code is required")
		return
	}

	rawToken, err := h.authSvc.RedeemClaimCode(req.Code)
	if err != nil {
		h.log.Warn("claim code redemption failed", "error", err)
		respondError(w, http.StatusNotFound, "invalid or expired claim code")
		return
	}

	respondOK(w, map[string]string{"token": rawToken})
}
