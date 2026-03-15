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

type PageAuthHandlers struct {
	authSvc       *service.AuthService
	settingsSvc   *service.SettingsService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageAuthHandlers(authSvc *service.AuthService, settingsSvc *service.SettingsService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageAuthHandlers {
	return &PageAuthHandlers{authSvc: authSvc, settingsSvc: settingsSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

// LoginPage renders the login form.
func (h *PageAuthHandlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, r, "auth/login", map[string]any{})
}

// Login handles the login form submission — sets a cookie and redirects.
func (h *PageAuthHandlers) Login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	rawToken := r.FormValue("token")
	if rawToken == "" {
		h.renderer.Render(w, r, "auth/login", map[string]any{
			"Error": "Token is required",
		})
		return
	}

	token := h.authSvc.ValidateToken(rawToken)
	if token == nil {
		h.renderer.Render(w, r, "auth/login", map[string]any{
			"Error": "Invalid token",
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "_token",
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400 * 30, // 30 days
	})

	w.Header().Set("HX-Redirect", "/")
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Logout clears the auth cookie and redirects to login.
func (h *PageAuthHandlers) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     "_token",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// TokensPage renders the token management page (admin only).
func (h *PageAuthHandlers) TokensPage(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.authSvc.ListTokens()
	if err != nil {
		h.log.Error("listing tokens", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if tokens == nil {
		tokens = []models.Token{}
	}

	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for token page", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}

	// Parse permissions and gameserver IDs for display
	type tokenView struct {
		models.Token
		PermissionsList   []string
		GameserverIDsList []string
	}
	var views []tokenView
	for _, t := range tokens {
		v := tokenView{Token: t}
		json.Unmarshal(t.Permissions, &v.PermissionsList)
		json.Unmarshal(t.GameserverIDs, &v.GameserverIDsList)
		views = append(views, v)
	}

	h.renderer.Render(w, r, "auth/tokens", map[string]any{
		"Tokens":         views,
		"Gameservers":    gameservers,
		"AllPermissions": service.AllPermissions,
		"AuthEnabled":    h.settingsSvc.GetAuthEnabled(),
		"AuthFromEnv":    h.settingsSvc.IsAuthEnabledFromEnv(),
	})
}

// CreateToken handles token creation from the web UI form.
func (h *PageAuthHandlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	gameserverIDs := r.Form["gameserver_ids"]
	permissions := r.Form["permissions"]

	var expiresAt *time.Time
	if exp := r.FormValue("expires_in"); exp != "" {
		d, err := time.ParseDuration(exp)
		if err == nil {
			t := time.Now().Add(d)
			expiresAt = &t
		}
	}

	rawToken, _, err := h.authSvc.CreateScopedToken(name, gameserverIDs, permissions, expiresAt)
	if err != nil {
		h.log.Error("creating token from web", "error", err)
		http.Error(w, "Failed to create token: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Show the raw token once — user must copy it
	h.renderer.Render(w, r, "auth/token_created", map[string]any{
		"RawToken": rawToken,
		"Name":     name,
	})
}

// DeleteToken handles token deletion from the web UI.
func (h *PageAuthHandlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "tokenId")
	if err := h.authSvc.DeleteToken(id); err != nil {
		h.log.Error("deleting token from web", "id", id, "error", err)
		http.Error(w, "Failed to delete token: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Re-render the tokens list
	w.Header().Set("HX-Push-Url", "false")
	h.TokensPage(w, r)
}

// EnableAuth handles enabling auth — generates admin token and shows it.
func (h *PageAuthHandlers) EnableAuth(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsAuthEnabledFromEnv() {
		http.Error(w, "Auth is controlled by environment variable", http.StatusBadRequest)
		return
	}

	rawToken, err := h.authSvc.GenerateAdminToken()
	if err != nil {
		h.log.Error("generating admin token", "error", err)
		http.Error(w, "Failed to generate admin token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := h.settingsSvc.SetAuthEnabled(true); err != nil {
		h.log.Error("enabling auth", "error", err)
		http.Error(w, "Failed to enable auth: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("auth enabled, admin token generated")

	h.renderer.Render(w, r, "auth/token_created", map[string]any{
		"RawToken":  rawToken,
		"Name":      "Admin",
		"IsAdmin":   true,
		"FirstTime": true,
	})
}

// DisableAuth handles disabling auth.
func (h *PageAuthHandlers) DisableAuth(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc.IsAuthEnabledFromEnv() {
		http.Error(w, "Auth is controlled by environment variable", http.StatusBadRequest)
		return
	}

	if err := h.settingsSvc.SetAuthEnabled(false); err != nil {
		h.log.Error("disabling auth", "error", err)
		http.Error(w, "Failed to disable auth: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.log.Info("auth disabled")

	w.Header().Set("HX-Push-Url", "false")
	h.TokensPage(w, r)
}
