package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

type Token struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	HashedToken    string     `json:"-"`
	TokenPrefix    string     `json:"-"`
	Role           string     `json:"role"`
	MaxGameservers *int       `json:"max_gameservers,omitempty"`
	MaxMemoryMB    *int       `json:"max_memory_mb,omitempty"`
	MaxCPU         *float64   `json:"max_cpu,omitempty"`
	MaxStorageMB   *int       `json:"max_storage_mb,omitempty"`
	ClaimCode      *string    `json:"claim_code,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// CanCreate returns true if this token has any quota set, meaning it can create gameservers.
func (t *Token) CanCreate() bool {
	return t.MaxGameservers != nil || t.MaxMemoryMB != nil || t.MaxCPU != nil || t.MaxStorageMB != nil
}

// GrantMap maps gameserver IDs to permission lists.
// Empty permission list = all gameserver permissions on that server.
// Absent key = no access to that server.
type GrantMap map[string][]string

func (g *GrantMap) Scan(src any) error {
	if src == nil {
		*g = GrantMap{}
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("grant_map: unsupported scan type %T", src)
	}
	var parsed GrantMap
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("grant_map: invalid JSON %q: %w", string(data), err)
	}
	*g = parsed
	return nil
}

func (g GrantMap) Value() (driver.Value, error) {
	if g == nil {
		return "{}", nil
	}
	data, err := json.Marshal(g)
	if err != nil {
		return nil, fmt.Errorf("grant_map: marshal error: %w", err)
	}
	return string(data), nil
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
