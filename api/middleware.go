package api

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/model"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// TokenFromContext returns the authenticated token from the request context, or nil.
// Delegates to auth.TokenFromContext.
var TokenFromContext = auth.TokenFromContext

// AuthMiddleware checks for a valid token on every request when auth is enabled.
// Extracts from Bearer header or _token cookie.
func AuthMiddleware(authSvc *auth.AuthService, settingsSvc *settings.SettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !settingsSvc.GetBool(settings.SettingAuthEnabled) {
				next.ServeHTTP(w, r)
				return
			}

			if settingsSvc.GetBool(settings.SettingLocalhostBypass) && isLocalhost(r) {
				next.ServeHTTP(w, r)
				return
			}

			rawToken := extractToken(r)
			if rawToken == "" {
				handleUnauthorized(w, r)
				return
			}

			token := authSvc.ValidateToken(rawToken)
			if token == nil {
				handleUnauthorized(w, r)
				return
			}

			ctx := auth.SetTokenInContext(r.Context(), token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func extractToken(r *http.Request) string {
	// Bearer token from Authorization header (API clients)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}

	// Cookie (web UI)
	if cookie, err := r.Cookie("_token"); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	return ""
}

func isLocalhost(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	return ip.IsLoopback()
}

// RequireAdmin returns 403 if the token is not an admin token.
// No-op when auth is disabled or localhost bypass is active (no token in context).
func RequireAdmin(settingsSvc *settings.SettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			// No token means auth is disabled or localhost bypass — allow through
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !auth.IsAdmin(token) {
				handleForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireClusterPermission returns 403 if the token doesn't have the given cluster permission.
// Unlike RequirePermission, this doesn't check gameserver IDs — it's for cluster-level routes.
func RequireClusterPermission(settingsSvc *settings.SettingsService, permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if auth.IsAdmin(token) {
				next.ServeHTTP(w, r)
				return
			}
			// gameserver.create is the only cluster-level permission for user tokens.
			// It's implied by having quotas set (can create within limits).
			if permission == auth.PermGameserverCreate && token.CanCreate() {
				next.ServeHTTP(w, r)
				return
			}
			handleForbidden(w, r)
		})
	}
}

// GameserverAccessChecker looks up ownership and grants for a gameserver.
// Used by permission middleware without coupling to the full store.
type GameserverAccessChecker interface {
	GetGameserverOwner(gameserverID string) (*string, error)
	GetGameserverGrants(gameserverID string) (model.GrantMap, error)
}

// tokenCanAccessGameserver checks if a token can access a gameserver via
// ownership (created_by_token_id) or grants (on the gameserver).
func tokenCanAccessGameserver(token *model.Token, gsID string, ac GameserverAccessChecker) bool {
	if ac == nil {
		return false
	}
	// Check ownership
	owner, err := ac.GetGameserverOwner(gsID)
	if err == nil && owner != nil && *owner == token.ID {
		return true
	}
	// Check grants on the gameserver
	grants, err := ac.GetGameserverGrants(gsID)
	if err == nil {
		if _, granted := grants[token.ID]; granted {
			return true
		}
	}
	return false
}

// RequirePermission returns 403 if the token doesn't have the given permission
// on the gameserver identified by the {id} URL parameter.
// Owners get all gameserver-scoped permissions. Granted tokens check the permission list.
// No-op when auth is disabled or localhost bypass is active.
func RequirePermission(settingsSvc *settings.SettingsService, ac GameserverAccessChecker, permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if auth.IsAdmin(token) {
				next.ServeHTTP(w, r)
				return
			}
			gsID := chi.URLParam(r, "id")
			if gsID == "" || ac == nil {
				handleForbidden(w, r)
				return
			}

			// Check ownership — owners get all gameserver permissions
			owner, err := ac.GetGameserverOwner(gsID)
			if err == nil && owner != nil && *owner == token.ID {
				next.ServeHTTP(w, r)
				return
			}

			// Check grants on the gameserver
			grants, err := ac.GetGameserverGrants(gsID)
			if err != nil || grants == nil {
				handleForbidden(w, r)
				return
			}
			grantPerms, granted := grants[token.ID]
			if !granted || !auth.HasGrantPermission(grantPerms, permission) {
				handleForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireGameserverAccess returns 403 if the token doesn't have any access
// to the gameserver identified by the {id} URL parameter (via ownership or grants).
func RequireGameserverAccess(settingsSvc *settings.SettingsService, ac GameserverAccessChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if auth.IsAdmin(token) {
				next.ServeHTTP(w, r)
				return
			}
			gsID := chi.URLParam(r, "id")
			if gsID == "" {
				handleForbidden(w, r)
				return
			}
			if tokenCanAccessGameserver(token, gsID, ac) {
				next.ServeHTTP(w, r)
				return
			}
			handleForbidden(w, r)
		})
	}
}

func handleForbidden(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"status":"error","error":"forbidden"}`))
}

func handleUnauthorized(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	w.Write([]byte(`{"status":"error","error":"unauthorized"}`))
}
