package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

type Token struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	HashedToken   string      `json:"-"`
	TokenPrefix   string      `json:"-"`
	Scope         string      `json:"scope"`
	GameserverIDs StringSlice `json:"gameserver_ids"`
	Permissions   StringSlice `json:"permissions"`
	ClaimCode     *string     `json:"claim_code,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	LastUsedAt    *time.Time  `json:"last_used_at,omitempty"`
	ExpiresAt     *time.Time  `json:"expires_at,omitempty"`
}

// StringSlice is a []string stored as JSON in the database.
type StringSlice []string

func (s *StringSlice) Scan(src any) error {
	if src == nil {
		*s = StringSlice{}
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("string_slice: unsupported scan type %T", src)
	}
	var parsed StringSlice
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("string_slice: invalid JSON %q: %w", string(data), err)
	}
	*s = parsed
	return nil
}

func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	data, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("string_slice: marshal error: %w", err)
	}
	return string(data), nil
}
