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
	gsQuery GameserverQuerier
	log     *slog.Logger
}

func NewAuthHandlers(authSvc *auth.AuthService, gsQuery GameserverQuerier, log *slog.Logger) *AuthHandlers {
	return &AuthHandlers{authSvc: authSvc, gsQuery: gsQuery, log: log}
}

func (h *AuthHandlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	role := r.URL.Query().Get("role")

	var tokens []model.Token
	var err error
	if role != "" {
		tokens, err = h.authSvc.ListTokensByRole(role)
	} else {
		tokens, err = h.authSvc.ListTokens()
	}
	if err != nil {
		h.log.Error("listing tokens", "role", role, "error", err)
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
		Name           string   `json:"name"`
		Role           string   `json:"role"`
		CanCreate      bool     `json:"can_create"`
		ExpiresIn      string   `json:"expires_in"` // e.g. "720h" for 30 days, empty = never
		MaxGameservers *int     `json:"max_gameservers,omitempty"`
		MaxMemoryMB    *int     `json:"max_memory_mb,omitempty"`
		MaxCPU         *float64 `json:"max_cpu,omitempty"`
		MaxStorageMB   *int     `json:"max_storage_mb,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Role == "" {
		req.Role = "user"
	}
	if req.Role != "admin" && req.Role != "user" && req.Role != "worker" {
		respondError(w, http.StatusBadRequest, "role must be \"admin\", \"user\", or \"worker\"")
		return
	}

	if req.Role == "worker" {
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

	if req.Role == "admin" {
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

	var quotas *auth.UserTokenQuotas
	if req.MaxGameservers != nil || req.MaxMemoryMB != nil || req.MaxCPU != nil || req.MaxStorageMB != nil {
		quotas = &auth.UserTokenQuotas{
			MaxGameservers: req.MaxGameservers,
			MaxMemoryMB:    req.MaxMemoryMB,
			MaxCPU:         req.MaxCPU,
			MaxStorageMB:   req.MaxStorageMB,
		}
	}
	rawToken, token, err := h.authSvc.CreateUserToken(req.Name, req.CanCreate, expiresAt, quotas)
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
	if token.Role != "worker" {
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

// Me returns the calling token's role, permissions, and quota usage.
func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromContext(r.Context())
	if token == nil {
		respondOK(w, map[string]any{
			"role": "admin",
		})
		return
	}

	resp := map[string]any{
		"role":       token.Role,
		"token_id":   token.ID,
		"can_create": token.CanCreate(),
	}

	// Include quota info for user tokens
	if token.Role == auth.RoleUser && h.gsQuery != nil {
		count, _ := h.gsQuery.CountGameserversByToken(token.ID)
		memUsed, cpuUsed, storageUsed, _ := h.gsQuery.SumResourcesByToken(token.ID)
		resp["quotas"] = map[string]any{
			"max_gameservers":  token.MaxGameservers,
			"max_memory_mb":    token.MaxMemoryMB,
			"max_cpu":          token.MaxCPU,
			"max_storage_mb":   token.MaxStorageMB,
			"used_gameservers": count,
			"used_memory_mb":   memUsed,
			"used_cpu":         cpuUsed,
			"used_storage_mb":  storageUsed,
		}
	}

	respondOK(w, resp)
}
