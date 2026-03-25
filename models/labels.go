package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
)

// Labels is a key-value map stored as JSON in the database.
// Used for worker node tags and gameserver placement constraints.
type Labels map[string]string

// Scan implements sql.Scanner for reading JSON labels from SQLite.
// SQLite returns JSON columns as strings, so we handle both string and []byte.
func (l *Labels) Scan(src any) error {
	if src == nil {
		*l = Labels{}
		return nil
	}

	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("labels: unsupported scan type %T", src)
	}

	parsed := Labels{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("labels: invalid JSON %q: %w", string(data), err)
	}
	*l = parsed
	return nil
}

// Value implements driver.Valuer for writing JSON labels to SQLite.
func (l Labels) Value() (driver.Value, error) {
	if l == nil {
		return "{}", nil
	}
	data, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("labels: marshal error: %w", err)
	}
	return string(data), nil
}

// HasAll returns true if l contains every key-value pair in required.
func (l Labels) HasAll(required Labels) bool {
	for k, v := range required {
		if l[k] != v {
			return false
		}
	}
	return true
}

// IsEmpty returns true if the labels map has no entries.
func (l Labels) IsEmpty() bool {
	return len(l) == 0
}
