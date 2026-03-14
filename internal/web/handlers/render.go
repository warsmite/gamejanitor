package handlers

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/netinfo"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/web/templates"
	"github.com/gorilla/csrf"
)

type Renderer struct {
	templates   map[string]*template.Template
	netInfo     *netinfo.Info
	settingsSvc *service.SettingsService
}

func NewRenderer(netInfo *netinfo.Info, settingsSvc *service.SettingsService) (*Renderer, error) {
	funcMap := template.FuncMap{
		"statusColor": statusColor,
		"formatTime":  formatTime,
		"jsonPretty":  jsonPretty,
		"rawJS":       func(s string) template.JS { return template.JS(s) },
		"lower":       strings.ToLower,
		"join":        strings.Join,
		"deref":       func(s *string) string { if s != nil { return *s }; return "" },
		"derefTime":   func(t *time.Time) time.Time { if t != nil { return *t }; return time.Time{} },
		"formatBytes": formatBytes,
		"queryJSON":   queryJSON,
		"multiply":    func(a, b int) int { return a * b },
		"gamePort":    func(portsJSON json.RawMessage) string { return firstGamePort(portsJSON) },
	}

	// Parse partials and layout as the base template set
	base, err := template.New("base").Funcs(funcMap).ParseFS(templates.Files, "layout.html", "partials/*.html")
	if err != nil {
		return nil, fmt.Errorf("parsing base templates: %w", err)
	}

	r := &Renderer{templates: make(map[string]*template.Template), netInfo: netInfo, settingsSvc: settingsSvc}

	// Find all page templates (top-level and subdirectories, excluding partials and layout)
	pages := []string{
		"error.html",
		"dashboard.html",
		"games/list.html",
		"games/detail.html",
		"games/new.html",
		"games/edit.html",
		"gameservers/form.html",
		"gameservers/detail.html",
		"gameservers/console.html",
		"gameservers/files.html",
		"gameservers/schedules.html",
		"gameservers/backups.html",
	}

	for _, page := range pages {
		t, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning base for %s: %w", page, err)
		}

		content, err := fs.ReadFile(templates.Files, page)
		if err != nil {
			return nil, fmt.Errorf("reading template %s: %w", page, err)
		}

		if _, err := t.Parse(string(content)); err != nil {
			return nil, fmt.Errorf("parsing template %s: %w", page, err)
		}

		// Key is the page name without .html extension
		name := strings.TrimSuffix(page, ".html")
		r.templates[name] = t
	}

	return r, nil
}

// Render executes a named template. If the request has HX-Request header,
// only the "content" block is rendered (for HTMX partial updates).
func (r *Renderer) Render(w http.ResponseWriter, req *http.Request, name string, data any) {
	t, ok := r.templates[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}

	// Inject CSRF token, network info, and connection address into template data
	if m, ok := data.(map[string]any); ok {
		m["CSRFToken"] = csrf.Token(req)
		m["NetInfo"] = r.netInfo
		m["ConnectionAddress"] = r.settingsSvc.GetConnectionAddress()
		m["ConnectionAddressConfigured"] = r.settingsSvc.IsConnectionAddressConfigured()
		m["ConnectionAddressFromEnv"] = r.settingsSvc.IsConnectionAddressFromEnv()
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "HX-Request")
	w.Header().Set("Cache-Control", "no-store")

	templateName := "layout.html"
	if req.Header.Get("HX-Request") == "true" {
		if req.Header.Get("HX-Target") == "gs-content" {
			templateName = "gs_content"
		} else {
			templateName = "content"
		}
	}

	if err := t.ExecuteTemplate(w, templateName, data); err != nil {
		http.Error(w, "rendering template: "+err.Error(), http.StatusInternalServerError)
	}
}

// RenderPartial executes a specific named template block (for HTMX partial responses).
func (r *Renderer) RenderPartial(w http.ResponseWriter, name string, block string, data any) {
	t, ok := r.templates[name]
	if !ok {
		http.Error(w, "template not found: "+name, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")

	if err := t.ExecuteTemplate(w, block, data); err != nil {
		http.Error(w, "rendering partial: "+err.Error(), http.StatusInternalServerError)
	}
}

// RenderError renders a styled error page with the given HTTP status code.
func (r *Renderer) RenderError(w http.ResponseWriter, req *http.Request, statusCode int) {
	heading, message := errorContent(statusCode)
	data := map[string]any{
		"StatusCode": statusCode,
		"Heading":    heading,
		"Message":    message,
	}

	t, ok := r.templates["error"]
	if !ok {
		http.Error(w, fmt.Sprintf("%d %s", statusCode, heading), statusCode)
		return
	}

	data["CSRFToken"] = csrf.Token(req)
	data["NetInfo"] = r.netInfo
	data["ConnectionAddress"] = r.settingsSvc.GetConnectionAddress()
	data["ConnectionAddressConfigured"] = r.settingsSvc.IsConnectionAddressConfigured()
	data["ConnectionAddressFromEnv"] = r.settingsSvc.IsConnectionAddressFromEnv()

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)

	templateName := "layout.html"
	if req.Header.Get("HX-Request") == "true" {
		templateName = "content"
	}

	if err := t.ExecuteTemplate(w, templateName, data); err != nil {
		http.Error(w, fmt.Sprintf("%d %s", statusCode, heading), statusCode)
	}
}

func errorContent(statusCode int) (heading string, message string) {
	switch statusCode {
	case http.StatusNotFound:
		return "Page Not Found", "Whatever you were looking for isn't here. It may have been moved or never existed."
	case http.StatusForbidden:
		return "Access Denied", "You don't have permission to access this page."
	case http.StatusInternalServerError:
		return "Something Broke", "An internal error occurred. Check the logs for details."
	case http.StatusBadRequest:
		return "Bad Request", "The request couldn't be understood. Try again or head back to the dashboard."
	default:
		return http.StatusText(statusCode), "Something unexpected happened."
	}
}

func statusColor(status string) string {
	switch status {
	case "running":
		return "green"
	case "started", "starting", "pulling":
		return "yellow"
	case "stopping":
		return "orange"
	case "stopped":
		return "gray"
	case "error":
		return "red"
	default:
		return "gray"
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// queryJSON serializes any value to a JS-safe JSON string for embedding in templates.
func queryJSON(v any) template.JS {
	b, err := json.Marshal(v)
	if err != nil {
		return template.JS("null")
	}
	return template.JS(b)
}

func jsonPretty(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}
