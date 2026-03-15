package web

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type contextKey string

const tokenContextKey contextKey = "auth_token"

// TokenFromContext returns the authenticated token from the request context, or nil.
func TokenFromContext(ctx context.Context) *models.Token {
	t, _ := ctx.Value(tokenContextKey).(*models.Token)
	return t
}

// AuthMiddleware checks for a valid token on every request when auth is enabled.
// Extracts from Bearer header (API) or _token cookie (web UI).
func AuthMiddleware(authSvc *service.AuthService, settingsSvc *service.SettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !settingsSvc.GetAuthEnabled() {
				next.ServeHTTP(w, r)
				return
			}

			if settingsSvc.GetLocalhostBypass() && isLocalhost(r) {
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

			ctx := context.WithValue(r.Context(), tokenContextKey, token)
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
func RequireAdmin(settingsSvc *service.SettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			// No token means auth is disabled or localhost bypass — allow through
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !service.IsAdmin(token) {
				handleForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequirePermission returns 403 if the token doesn't have the given permission
// on the gameserver identified by the {id} URL parameter.
// No-op when auth is disabled or localhost bypass is active.
func RequirePermission(settingsSvc *service.SettingsService, permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if service.IsAdmin(token) {
				next.ServeHTTP(w, r)
				return
			}
			gsID := chi.URLParam(r, "id")
			if gsID == "" || !service.HasPermission(token, gsID, permission) {
				handleForbidden(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireGameserverAccess returns 403 if the token doesn't have any permission
// on the gameserver identified by the {id} URL parameter. Used for view-only routes.
func RequireGameserverAccess(settingsSvc *service.SettingsService) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := TokenFromContext(r.Context())
			if token == nil {
				next.ServeHTTP(w, r)
				return
			}
			if service.IsAdmin(token) {
				next.ServeHTTP(w, r)
				return
			}
			gsID := chi.URLParam(r, "id")
			if gsID == "" {
				handleForbidden(w, r)
				return
			}
			var gsIDs []string
			if err := json.Unmarshal(token.GameserverIDs, &gsIDs); err != nil {
				handleForbidden(w, r)
				return
			}
			for _, id := range gsIDs {
				if id == gsID {
					next.ServeHTTP(w, r)
					return
				}
			}
			handleForbidden(w, r)
		})
	}
}

func handleForbidden(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"forbidden"}`))
		return
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func handleUnauthorized(w http.ResponseWriter, r *http.Request) {
	// API requests get a JSON 401
	if strings.HasPrefix(r.URL.Path, "/api/") {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}

	// Web UI requests get redirected to login
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}
