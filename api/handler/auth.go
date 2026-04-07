package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/auth"
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

type tokenResponse struct {
	Token   string `json:"token,omitempty"`
	TokenID string `json:"token_id"`
	Name    string `json:"name"`
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

	var rawToken string
	var token *model.Token
	var err error

	switch req.Role {
	case "worker":
		rawToken, token, err = h.authSvc.CreateWorkerToken(req.Name)
	case "admin":
		rawToken, token, err = h.authSvc.CreateAdminToken(req.Name)
	case "user":
		rawToken, token, err = h.createUserToken(req)
	}
	if err != nil {
		h.log.Error("creating token", "role", req.Role, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	// Worker/admin tokens are idempotent — empty rawToken means it already existed
	if rawToken == "" {
		respondError(w, http.StatusConflict, "token \""+token.Name+"\" already exists")
		return
	}

	respondCreated(w, tokenResponse{Token: rawToken, TokenID: token.ID, Name: token.Name})
}

func (h *AuthHandlers) createUserToken(req struct {
	Name           string   `json:"name"`
	Role           string   `json:"role"`
	CanCreate      bool     `json:"can_create"`
	ExpiresIn      string   `json:"expires_in"`
	MaxGameservers *int     `json:"max_gameservers,omitempty"`
	MaxMemoryMB    *int     `json:"max_memory_mb,omitempty"`
	MaxCPU         *float64 `json:"max_cpu,omitempty"`
	MaxStorageMB   *int     `json:"max_storage_mb,omitempty"`
}) (string, *model.Token, error) {
	var expiresAt *time.Time
	if req.ExpiresIn != "" {
		d, err := time.ParseDuration(req.ExpiresIn)
		if err != nil {
			return "", nil, &controller.ServiceError{Code: http.StatusBadRequest, Message: "invalid expires_in duration: " + err.Error()}
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

	return h.authSvc.CreateUserToken(req.Name, req.CanCreate, expiresAt, quotas)
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
		respondError(w, http.StatusNotFound, "token not found")
		return
	}
	if token.Role != "worker" {
		respondError(w, http.StatusBadRequest, "only worker tokens can be rotated")
		return
	}
	rawToken, newToken, err := h.authSvc.RotateWorkerToken(token.Name)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, tokenResponse{Token: rawToken, TokenID: newToken.ID, Name: newToken.Name})
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
	respondOK(w, struct {
		ClaimCode string `json:"claim_code"`
	}{ClaimCode: code})
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

	respondOK(w, struct {
		Token string `json:"token"`
	}{Token: rawToken})
}

type meResponse struct {
	Role      string    `json:"role"`
	TokenID   string    `json:"token_id,omitempty"`
	CanCreate bool      `json:"can_create,omitempty"`
	Quotas    *meQuotas `json:"quotas,omitempty"`
}

type meQuotas struct {
	MaxGameservers *int     `json:"max_gameservers"`
	MaxMemoryMB    *int     `json:"max_memory_mb"`
	MaxCPU         *float64 `json:"max_cpu"`
	MaxStorageMB   *int     `json:"max_storage_mb"`
	UsedGameservers int     `json:"used_gameservers"`
	UsedMemoryMB    int     `json:"used_memory_mb"`
	UsedCPU         float64 `json:"used_cpu"`
	UsedStorageMB   int     `json:"used_storage_mb"`
}

// Me returns the calling token's role, permissions, and quota usage.
func (h *AuthHandlers) Me(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromContext(r.Context())
	if token == nil {
		respondOK(w, meResponse{Role: "admin"})
		return
	}

	resp := meResponse{
		Role:      token.Role,
		TokenID:   token.ID,
		CanCreate: token.CanCreate(),
	}

	// Include quota info for user tokens
	if token.Role == auth.RoleUser && h.gsQuery != nil {
		count, _ := h.gsQuery.CountGameserversByToken(token.ID)
		memUsed, cpuUsed, storageUsed, _ := h.gsQuery.SumResourcesByToken(token.ID)
		resp.Quotas = &meQuotas{
			MaxGameservers:  token.MaxGameservers,
			MaxMemoryMB:     token.MaxMemoryMB,
			MaxCPU:          token.MaxCPU,
			MaxStorageMB:    token.MaxStorageMB,
			UsedGameservers: count,
			UsedMemoryMB:    memUsed,
			UsedCPU:         cpuUsed,
			UsedStorageMB:   storageUsed,
		}
	}

	respondOK(w, resp)
}
