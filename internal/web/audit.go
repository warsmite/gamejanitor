package web

import (
	"database/sql"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AuditMiddleware logs all mutating requests (POST/PUT/DELETE) to the audit_log table.
func AuditMiddleware(db *sql.DB, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			sw := &statusWriter{ResponseWriter: w, statusCode: http.StatusOK}
			next.ServeHTTP(sw, r)

			pattern := chi.RouteContext(r.Context()).RoutePattern()
			if pattern == "" {
				return
			}

			action, resourceType, resourceIDParam := deriveAuditAction(r.Method, pattern)
			if action == "" {
				return
			}

			resourceID := ""
			if resourceIDParam != "" {
				resourceID = chi.URLParam(r, resourceIDParam)
			}

			tokenID := ""
			tokenName := ""
			if token := TokenFromContext(r.Context()); token != nil {
				tokenID = token.ID
				tokenName = token.Name
			}

			ip, _, _ := net.SplitHostPort(r.RemoteAddr)
			if ip == "" {
				ip = r.RemoteAddr
			}

			entry := &models.AuditLog{
				ID:           uuid.New().String(),
				Timestamp:    time.Now(),
				Action:       action,
				ResourceType: resourceType,
				ResourceID:   resourceID,
				TokenID:      tokenID,
				TokenName:    tokenName,
				IPAddress:    ip,
				StatusCode:   sw.statusCode,
			}

			if err := models.CreateAuditLog(db, entry); err != nil {
				log.Error("failed to write audit log", "action", action, "error", err)
			}
		})
	}
}

type statusWriter struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.statusCode = code
		w.written = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap supports http.ResponseController and middleware that check for wrapped writers.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// deriveAuditAction maps an HTTP method + Chi route pattern to an audit action name,
// resource type, and the URL param name that holds the resource ID.
// Returns empty action if the route should not be audited.
func deriveAuditAction(method, pattern string) (action, resourceType, resourceIDParam string) {
	// Normalize: strip trailing slash, handle both /api and page route prefixes
	p := strings.TrimSuffix(pattern, "/")

	// Strip common prefixes to get the semantic route
	p = strings.TrimPrefix(p, "/api")

	// Skip routes that aren't meaningful mutations
	switch {
	case p == "/login" || p == "/logout":
		return "", "", ""
	case strings.HasPrefix(p, "/static"):
		return "", "", ""
	}

	// Explicit mappings for special action routes
	type mapping struct {
		action       string
		resourceType string
		idParam      string
	}

	actionRoutes := map[string]mapping{
		"POST /gameservers/{id}/start":                      {"gameserver.start", "gameserver", "id"},
		"POST /gameservers/{id}/stop":                       {"gameserver.stop", "gameserver", "id"},
		"POST /gameservers/{id}/restart":                    {"gameserver.restart", "gameserver", "id"},
		"POST /gameservers/{id}/update-game":                {"gameserver.update-game", "gameserver", "id"},
		"POST /gameservers/{id}/reinstall":                  {"gameserver.reinstall", "gameserver", "id"},
		"POST /gameservers/{id}/migrate":                    {"gameserver.migrate", "gameserver", "id"},
		"POST /gameservers/{id}/regenerate-sftp-password":   {"gameserver.regenerate-sftp-password", "gameserver", "id"},
		"POST /gameservers/{id}/command":                    {"gameserver.command", "gameserver", "id"},
		"POST /gameservers/{id}/console/command":            {"gameserver.command", "gameserver", "id"},
		"POST /gameservers/bulk":                            {"gameserver.bulk", "gameserver", ""},
		"POST /gameservers/{id}/backups/{backupId}/restore": {"backup.restore", "backup", "backupId"},
		"POST /gameservers/{id}/schedules/{scheduleId}/toggle": {"schedule.toggle", "schedule", "scheduleId"},
		"POST /settings/connection-address":                    {"settings.update", "settings", ""},
		"DELETE /settings/connection-address":                   {"settings.update", "settings", ""},
		"POST /settings/port-range":                            {"settings.update", "settings", ""},
		"POST /settings/port-mode":                             {"settings.update", "settings", ""},
		"POST /settings/max-backups":                           {"settings.update", "settings", ""},
		"POST /settings/audit-retention":                       {"settings.update", "settings", ""},
		"POST /settings/localhost-bypass/enable":               {"settings.localhost-bypass", "settings", ""},
		"POST /settings/localhost-bypass/disable":              {"settings.localhost-bypass", "settings", ""},
		"POST /settings/auth/enable":                           {"auth.enable", "settings", ""},
		"POST /settings/auth/disable":                          {"auth.disable", "settings", ""},
		"POST /settings/workers/{workerID}/port-range":        {"worker.update", "worker", "workerID"},
		"DELETE /settings/workers/{workerID}/port-range":      {"worker.update", "worker", "workerID"},
		"POST /settings/workers/{workerID}/limits":            {"worker.update", "worker", "workerID"},
		"DELETE /settings/workers/{workerID}/limits":          {"worker.update", "worker", "workerID"},
		"POST /settings/worker-tokens":                        {"worker-token.create", "token", ""},
		"DELETE /settings/worker-tokens/{tokenId}":            {"worker-token.delete", "token", "tokenId"},
		"POST /settings/tokens":                               {"token.create", "token", ""},
		"DELETE /settings/tokens/{tokenId}":                    {"token.delete", "token", "tokenId"},
		"PUT /settings":                                        {"settings.update", "settings", ""},
		"PUT /workers/{workerID}":                             {"worker.update", "worker", "workerID"},
		"PUT /workers/{workerID}/port-range":                  {"worker.update", "worker", "workerID"},
		"DELETE /workers/{workerID}/port-range":               {"worker.update", "worker", "workerID"},
		"PUT /workers/{workerID}/limits":                      {"worker.update", "worker", "workerID"},
		"DELETE /workers/{workerID}/limits":                   {"worker.update", "worker", "workerID"},
	}

	key := method + " " + p
	if m, ok := actionRoutes[key]; ok {
		return m.action, m.resourceType, m.idParam
	}

	// Generic derivation for standard CRUD routes
	return deriveGenericAction(method, p)
}

// deriveGenericAction handles standard REST patterns like:
// POST /gameservers → gameserver.create
// PUT /gameservers/{id} → gameserver.update
// DELETE /gameservers/{id} → gameserver.delete
func deriveGenericAction(method, path string) (action, resourceType, resourceIDParam string) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 0 {
		return "", "", ""
	}

	verb := ""
	switch method {
	case http.MethodPost:
		verb = "create"
	case http.MethodPut:
		verb = "update"
	case http.MethodDelete:
		verb = "delete"
	default:
		return "", "", ""
	}

	// Find the deepest resource in the path
	// e.g. /gameservers/{id}/schedules/{scheduleId} → schedule
	// e.g. /gameservers/{id} → gameserver
	// e.g. /tokens → token
	resource := ""
	idParam := ""
	for i := len(parts) - 1; i >= 0; i-- {
		if strings.HasPrefix(parts[i], "{") && strings.HasSuffix(parts[i], "}") {
			idParam = parts[i][1 : len(parts[i])-1]
			continue
		}
		resource = parts[i]
		break
	}

	if resource == "" {
		return "", "", ""
	}

	// Singularize common resource names
	singular := strings.TrimSuffix(resource, "s")
	if resource == "settings" {
		singular = "settings"
	}
	if strings.HasSuffix(resource, "ses") {
		singular = strings.TrimSuffix(resource, "es")
	}

	return singular + "." + verb, singular, idParam
}
