package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/pkg/validate"
)

// Pagination and file size limits — inlined from deleted constants/ package.
const (
	PaginationMaxLimit        = 200
	PaginationDefaultLimit    = 50
	PaginationDefaultLogTail  = 100
	PaginationDefaultModLimit = 20

	MaxFileWriteBytes    = 10 * 1024 * 1024  // 10 MB — inline file writes via API
	MaxFileUploadBytes   = 100 * 1024 * 1024 // 100 MB — multipart file uploads
	MaxModDownloadBytes  = 100 * 1024 * 1024 // 100 MB — mod downloads (Modrinth, Workshop, generic)
	MaxUmodDownloadBytes = 50 * 1024 * 1024  // 50 MB — uMod plugin downloads
)

type envelope struct {
	Status string `json:"status"`
	Data   any    `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

func respondOK(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(envelope{Status: "ok", Data: data})
}

func respondCreated(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(envelope{Status: "ok", Data: data})
}

func respondAccepted(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(envelope{Status: "ok", Data: data})
}

func respondNoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}

func respondError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(envelope{Status: "error", Error: message})
}

// parsePagination extracts optional limit/offset from query params.
func parsePagination(r *http.Request) model.Pagination {
	var p model.Pagination
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = min(n, PaginationMaxLimit)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			p.Offset = n
		}
	}
	return p
}

// serviceErrorStatus extracts the HTTP status code from a ServiceError, falling back to 500.
func serviceErrorStatus(err error) int {
	var valErr validate.FieldErrors
	if errors.As(err, &valErr) {
		return http.StatusBadRequest
	}
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Code
	}
	return http.StatusInternalServerError
}

// serviceErrorMessage returns a safe error message for API responses.
// ServiceErrors and validation errors are user-facing and safe to expose.
// Other errors are internal and get replaced with a generic message to avoid
// leaking implementation details.
func serviceErrorMessage(err error) string {
	var valErr validate.FieldErrors
	if errors.As(err, &valErr) {
		return valErr.Error()
	}
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Error()
	}
	return "internal server error"
}
