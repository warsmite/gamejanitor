package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/warsmite/gamejanitor/internal/service"
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

// serviceErrorStatus extracts the HTTP status code from a ServiceError, falling back to 500.
func serviceErrorStatus(err error) int {
	var svcErr *service.ServiceError
	if errors.As(err, &svcErr) {
		return svcErr.Code
	}
	return http.StatusInternalServerError
}
