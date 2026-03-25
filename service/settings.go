package service

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/validate"
)

// Setting key constants
const (
	SettingConnectionAddress   = "connection_address"
	SettingPortRangeStart      = "port_range_start"
	SettingPortRangeEnd        = "port_range_end"
	SettingPortUniqueness      = "port_uniqueness"
	SettingPortMode            = "port_mode"
	SettingMaxBackups          = "max_backups"
	SettingAuthEnabled         = "auth_enabled"
	SettingLocalhostBypass     = "localhost_bypass"
	SettingRateLimitEnabled    = "rate_limit_enabled"
	SettingRateLimitPerIP      = "rate_limit_per_ip"
	SettingRateLimitPerToken   = "rate_limit_per_token"
	SettingRateLimitLogin      = "rate_limit_login"
	SettingTrustProxyHeaders   = "trust_proxy_headers"
	SettingEventRetention      = "event_retention_days"
	SettingRequireMemoryLimit  = "require_memory_limit"
	SettingRequireCPULimit     = "require_cpu_limit"
	SettingRequireStorageLimit = "require_storage_limit"
)

// Mode constants for selecting default profiles.
const (
	ModeDefault  = ""
	ModeBusiness = "business"
)

// Defaults defines every setting with its default value.
// The Go type of the default IS the setting's type: bool, int, or string.
var Defaults = map[string]any{
	SettingConnectionAddress:   "",
	SettingPortRangeStart:      27000,
	SettingPortRangeEnd:        28999,
	SettingPortUniqueness:      "cluster",
	SettingPortMode:            "auto",
	SettingMaxBackups:          10,
	SettingAuthEnabled:         false,
	SettingLocalhostBypass:     true,
	SettingRateLimitEnabled:    false,
	SettingRateLimitPerIP:      20,
	SettingRateLimitPerToken:   10,
	SettingRateLimitLogin:      10,
	SettingTrustProxyHeaders:   false,
	SettingEventRetention:      30,
	SettingRequireMemoryLimit:  false,
	SettingRequireCPULimit:     false,
	SettingRequireStorageLimit: false,
}

// BusinessOverrides are applied on top of Defaults when mode is "business".
// Only settings that differ from the default profile need to be listed here.
var BusinessOverrides = map[string]any{
	SettingAuthEnabled:         true,
	SettingLocalhostBypass:     false,
	SettingRateLimitEnabled:    true,
	SettingRequireMemoryLimit:  true,
	SettingRequireCPULimit:     true,
	SettingRequireStorageLimit: true,
}

// DefaultsForMode returns the full defaults map for a given mode.
func DefaultsForMode(mode string) map[string]any {
	result := make(map[string]any, len(Defaults))
	for k, v := range Defaults {
		result[k] = v
	}
	if mode == ModeBusiness {
		for k, v := range BusinessOverrides {
			result[k] = v
		}
	}
	return result
}

// settingValidators defines per-key validation rules applied after type coercion.
// Keys without an entry need no validation beyond type checking.
var settingValidators = map[string]func(any) error{
	SettingPortRangeStart: validatePort,
	SettingPortRangeEnd:   validatePort,
	SettingPortUniqueness: validateOneOf("cluster", "node"),
	SettingPortMode:       validateOneOf("auto", "manual"),
	SettingMaxBackups:     validateMinInt(0),
	SettingRateLimitPerIP:    validateMinInt(1),
	SettingRateLimitPerToken: validateMinInt(1),
	SettingRateLimitLogin:    validateMinInt(1),
	SettingEventRetention:    validateMinInt(1),
}

func validatePort(v any) error {
	n, _ := v.(int)
	if n < 1 || n > 65535 {
		return fmt.Errorf("must be between 1 and 65535 (got %d)", n)
	}
	return nil
}

func validateMinInt(min int) func(any) error {
	return func(v any) error {
		n, _ := v.(int)
		if n < min {
			return fmt.Errorf("must be >= %d (got %d)", min, n)
		}
		return nil
	}
}

func validateOneOf(allowed ...string) func(any) error {
	return func(v any) error {
		s, _ := v.(string)
		for _, a := range allowed {
			if s == a {
				return nil
			}
		}
		return fmt.Errorf("must be one of %v (got %q)", allowed, s)
	}
}

type SettingsService struct {
	mu       sync.RWMutex
	values   map[string]any // live typed values, served from memory
	defaults map[string]any // the active defaults for this mode
	mode     string
	db       *sql.DB
	log      *slog.Logger
}

func NewSettingsService(db *sql.DB, log *slog.Logger) *SettingsService {
	return NewSettingsServiceWithMode(db, log, ModeDefault)
}

func NewSettingsServiceWithMode(db *sql.DB, log *slog.Logger, mode string) *SettingsService {
	defaults := DefaultsForMode(mode)

	s := &SettingsService{
		values:   make(map[string]any, len(defaults)),
		defaults: defaults,
		mode:     mode,
		db:       db,
		log:      log,
	}

	// Start with defaults for this mode
	for k, v := range defaults {
		s.values[k] = v
	}

	// Load persisted values from DB, overwriting defaults
	stored, err := models.AllSettings(db)
	if err != nil {
		log.Error("failed to load settings from DB, using defaults", "error", err)
		return s
	}
	for key, strVal := range stored {
		def, ok := defaults[key]
		if !ok {
			continue // ignore unknown keys in DB
		}
		if parsed, err := parseAs(strVal, def); err == nil {
			s.values[key] = parsed
		}
	}

	if mode != ModeDefault {
		log.Info("settings mode active", "mode", mode)
	}

	return s
}

// ApplyConfig writes config-specified settings to DB and memory on startup.
// Only keys present in the map are written — unspecified settings are left alone.
func (s *SettingsService) ApplyConfig(settings map[string]any) {
	if len(settings) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	applied := 0
	for key, val := range settings {
		def, ok := s.defaults[key]
		if !ok {
			s.log.Warn("ignoring unknown setting from config", "key", key)
			continue
		}

		// Coerce the YAML-parsed value to match the default's type
		typed, err := coerce(val, def)
		if err != nil {
			s.log.Warn("invalid config value for setting", "key", key, "value", val, "error", err)
			continue
		}

		if validator, ok := settingValidators[key]; ok {
			if err := validator(typed); err != nil {
				s.log.Warn("invalid config value for setting", "key", key, "value", val, "error", err)
				continue
			}
		}

		s.values[key] = typed

		// Persist to DB
		if err := models.SetSetting(s.db, key, fmt.Sprintf("%v", typed)); err != nil {
			s.log.Error("failed to persist config setting", "key", key, "error", err)
			continue
		}
		applied++
	}

	if applied > 0 {
		s.log.Info("applied config settings to DB", "count", applied)
	}
}

// GetBool returns a boolean setting. Returns the default if key is unknown.
func (s *SettingsService) GetBool(key string) bool {
	s.mu.RLock()
	v, ok := s.values[key]
	s.mu.RUnlock()
	if !ok {
		if d, ok := s.defaults[key]; ok {
			if b, ok := d.(bool); ok {
				return b
			}
		}
		return false
	}
	b, _ := v.(bool)
	return b
}

// GetInt returns an integer setting. Returns the default if key is unknown.
func (s *SettingsService) GetInt(key string) int {
	s.mu.RLock()
	v, ok := s.values[key]
	s.mu.RUnlock()
	if !ok {
		if d, ok := s.defaults[key]; ok {
			if n, ok := d.(int); ok {
				return n
			}
		}
		return 0
	}
	n, _ := v.(int)
	return n
}

// GetString returns a string setting. Returns the default if key is unknown.
func (s *SettingsService) GetString(key string) string {
	s.mu.RLock()
	v, ok := s.values[key]
	s.mu.RUnlock()
	if !ok {
		if d, ok := s.defaults[key]; ok {
			if str, ok := d.(string); ok {
				return str
			}
		}
		return ""
	}
	str, _ := v.(string)
	return str
}

// Set updates a setting in memory and persists to DB.
func (s *SettingsService) Set(key string, value any) error {
	def, ok := s.defaults[key]
	if !ok {
		return fmt.Errorf("unknown setting: %s", key)
	}

	typed, err := coerce(value, def)
	if err != nil {
		return fmt.Errorf("invalid value for %s: %w", key, err)
	}

	if err := s.validateSetting(key, typed); err != nil {
		return err
	}

	if err := models.SetSetting(s.db, key, fmt.Sprintf("%v", typed)); err != nil {
		return err
	}

	s.mu.Lock()
	s.values[key] = typed
	s.mu.Unlock()
	return nil
}

// validateSetting runs per-key validation and cross-field checks.
func (s *SettingsService) validateSetting(key string, typed any) error {
	if validator, ok := settingValidators[key]; ok {
		if err := validator(typed); err != nil {
			var fe validate.FieldErrors
			fe.Add(key, err.Error())
			return fe.Err()
		}
	}

	// Cross-field: port_range_start must be <= port_range_end
	if key == SettingPortRangeStart {
		start, _ := typed.(int)
		end := s.GetInt(SettingPortRangeEnd)
		if start > end {
			var fe validate.FieldErrors
			fe.Addf(key, "port_range_start (%d) must be <= port_range_end (%d)", start, end)
			return fe.Err()
		}
	}
	if key == SettingPortRangeEnd {
		end, _ := typed.(int)
		start := s.GetInt(SettingPortRangeStart)
		if start > end {
			var fe validate.FieldErrors
			fe.Addf(key, "port_range_end (%d) must be >= port_range_start (%d)", end, start)
			return fe.Err()
		}
	}

	return nil
}

// Clear removes a setting from DB and reverts to default in memory.
func (s *SettingsService) Clear(key string) error {
	if err := models.DeleteSetting(s.db, key); err != nil {
		return err
	}

	s.mu.Lock()
	if def, ok := s.defaults[key]; ok {
		s.values[key] = def
	} else {
		delete(s.values, key)
	}
	s.mu.Unlock()
	return nil
}

// All returns all settings with their current typed values.
func (s *SettingsService) All() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make(map[string]any, len(s.values))
	for k, v := range s.values {
		result[k] = v
	}
	return result
}

// IsKnown returns true if the key is a registered setting.
func (s *SettingsService) IsKnown(key string) bool {
	_, ok := s.defaults[key]
	return ok
}

// Mode returns the active settings mode.
func (s *SettingsService) Mode() string {
	return s.mode
}


// ResolveConnectionIP returns the connection IP for a gameserver on the given node.
// Priority: global override > worker external IP > worker LAN IP > empty (caller falls back to 127.0.0.1).
func (s *SettingsService) ResolveConnectionIP(nodeID *string) (ip string, configured bool) {
	if globalIP := s.GetString(SettingConnectionAddress); globalIP != "" {
		return globalIP, true
	}
	if nodeID != nil && *nodeID != "" {
		node, err := models.GetWorkerNode(s.db, *nodeID)
		if err == nil && node != nil {
			if node.ExternalIP != "" {
				return node.ExternalIP, true
			}
			if node.LanIP != "" {
				return node.LanIP, true
			}
		}
	}
	return "", false
}

// parseAs parses a DB string value into the same Go type as the default.
func parseAs(strVal string, defaultVal any) (any, error) {
	switch defaultVal.(type) {
	case bool:
		return strVal == "true", nil
	case int:
		n, err := strconv.Atoi(strVal)
		if err != nil {
			return nil, err
		}
		return n, nil
	case string:
		return strVal, nil
	default:
		return strVal, nil
	}
}

// coerce converts a value (from YAML, API, etc.) to match the default's Go type.
func coerce(val any, defaultVal any) (any, error) {
	switch defaultVal.(type) {
	case bool:
		switch v := val.(type) {
		case bool:
			return v, nil
		case string:
			return v == "true" || v == "1", nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to bool", val)
		}
	case int:
		switch v := val.(type) {
		case int:
			return v, nil
		case int64:
			return int(v), nil
		case float64:
			return int(v), nil
		case string:
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, err
			}
			return n, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to int", val)
		}
	case string:
		return fmt.Sprintf("%v", val), nil
	default:
		return val, nil
	}
}
