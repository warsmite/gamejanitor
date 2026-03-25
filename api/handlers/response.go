package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
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
func parsePagination(r *http.Request) models.Pagination {
	var p models.Pagination
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			p.Limit = min(n, constants.PaginationMaxLimit)
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
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Code
	}
	return http.StatusInternalServerError
}

// serviceErrorMessage returns a safe error message for API responses.
// ServiceErrors are user-facing and safe to expose. Other errors are internal
// and get replaced with a generic message to avoid leaking implementation details.
func serviceErrorMessage(err error) string {
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Error()
	}
	return "internal server error"
}
