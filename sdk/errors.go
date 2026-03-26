package gamejanitor

import "fmt"

// Error represents an API error response from the Gamejanitor server.
type Error struct {
	// StatusCode is the HTTP status code returned by the server.
	StatusCode int
	// Message is the error message from the API response.
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("gamejanitor: %d %s", e.StatusCode, e.Message)
}

// IsNotFound reports whether the error is a 404 Not Found response.
func IsNotFound(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.StatusCode == 404
	}
	return false
}

// IsForbidden reports whether the error is a 403 Forbidden response.
func IsForbidden(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.StatusCode == 403
	}
	return false
}

// IsUnauthorized reports whether the error is a 401 Unauthorized response.
func IsUnauthorized(err error) bool {
	if e, ok := err.(*Error); ok {
		return e.StatusCode == 401
	}
	return false
}
