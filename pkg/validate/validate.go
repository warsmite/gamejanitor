package validate

import (
	"fmt"
	"strings"
)

// FieldErrors collects validation failures across multiple fields.
// A nil FieldErrors means validation passed.
type FieldErrors []FieldError

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (fe FieldErrors) Error() string {
	msgs := make([]string, len(fe))
	for i, e := range fe {
		msgs[i] = e.Field + ": " + e.Message
	}
	return strings.Join(msgs, "; ")
}

// Err returns nil if there are no errors, otherwise returns the FieldErrors.
func (fe FieldErrors) Err() error {
	if len(fe) == 0 {
		return nil
	}
	return fe
}

// Add appends a validation error for a field.
func (fe *FieldErrors) Add(field, message string) {
	*fe = append(*fe, FieldError{Field: field, Message: message})
}

// Addf appends a formatted validation error for a field.
func (fe *FieldErrors) Addf(field, format string, args ...any) {
	fe.Add(field, fmt.Sprintf(format, args...))
}

// MinInt checks that val >= min.
func (fe *FieldErrors) MinInt(field string, val, min int) {
	if val < min {
		fe.Addf(field, "must be >= %d (got %d)", min, val)
	}
}

// MaxInt checks that val <= max.
func (fe *FieldErrors) MaxInt(field string, val, max int) {
	if val > max {
		fe.Addf(field, "must be <= %d (got %d)", max, val)
	}
}

// RangeInt checks that min <= val <= max.
func (fe *FieldErrors) RangeInt(field string, val, min, max int) {
	if val < min || val > max {
		fe.Addf(field, "must be between %d and %d (got %d)", min, max, val)
	}
}

// MinFloat checks that val >= min.
func (fe *FieldErrors) MinFloat(field string, val, min float64) {
	if val < min {
		fe.Addf(field, "must be >= %g (got %g)", min, val)
	}
}

// PortNumber checks that val is a valid port (1-65535).
func (fe *FieldErrors) PortNumber(field string, val int) {
	fe.RangeInt(field, val, 1, 65535)
}

// NotEmpty checks that a string is non-empty.
func (fe *FieldErrors) NotEmpty(field, val string) {
	if val == "" {
		fe.Addf(field, "must not be empty")
	}
}

// OneOf checks that val is one of the allowed values.
func (fe *FieldErrors) OneOf(field, val string, allowed []string) {
	for _, a := range allowed {
		if val == a {
			return
		}
	}
	fe.Addf(field, "must be one of [%s] (got %q)", strings.Join(allowed, ", "), val)
}

// MinIntPtr checks that *val >= min, if val is non-nil.
func (fe *FieldErrors) MinIntPtr(field string, val *int, min int) {
	if val != nil && *val < min {
		fe.Addf(field, "must be >= %d (got %d)", min, *val)
	}
}

// MinFloatPtr checks that *val >= min, if val is non-nil.
func (fe *FieldErrors) MinFloatPtr(field string, val *float64, min float64) {
	if val != nil && *val < min {
		fe.Addf(field, "must be >= %g (got %g)", min, *val)
	}
}
