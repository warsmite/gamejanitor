package service

import (
	"database/sql"
	"log/slog"
	"os"
	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

const (
	SettingConnectionAddress = "connection_address"
	SettingPortRangeStart    = "port_range_start"
	SettingPortRangeEnd      = "port_range_end"
	SettingPreferredPortMode = "preferred_port_mode"
	SettingMaxBackups        = "max_backups"
	SettingAuthEnabled       = "auth_enabled"
	SettingLocalhostBypass   = "localhost_bypass"

	DefaultPortRangeStart    = 27000
	DefaultPortRangeEnd      = 28999
	DefaultPreferredPortMode = "auto"
	DefaultMaxBackups        = 10
)

type SettingsService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewSettingsService(db *sql.DB, log *slog.Logger) *SettingsService {
	return &SettingsService{db: db, log: log}
}

// GetConnectionAddress returns the configured connection address.
// Priority: ENV var > DB setting > empty string (unconfigured).
func (s *SettingsService) GetConnectionAddress() string {
	if v := os.Getenv("GJ_CONNECTION_ADDRESS"); v != "" {
		return v
	}

	v, err := models.GetSetting(s.db, SettingConnectionAddress)
	if err != nil {
		s.log.Error("reading connection_address setting", "error", err)
		return ""
	}
	return v
}

// IsConnectionAddressConfigured returns true if a connection address is set via ENV or DB.
func (s *SettingsService) IsConnectionAddressConfigured() bool {
	return s.GetConnectionAddress() != ""
}

// IsConnectionAddressFromEnv returns true if the connection address is set via ENV (not editable from UI).
func (s *SettingsService) IsConnectionAddressFromEnv() bool {
	return os.Getenv("GJ_CONNECTION_ADDRESS") != ""
}

// SetConnectionAddress saves the connection address to the DB.
func (s *SettingsService) SetConnectionAddress(address string) error {
	s.log.Info("setting connection address", "address", address)
	return models.SetSetting(s.db, SettingConnectionAddress, address)
}

// ClearConnectionAddress removes the connection address from the DB.
func (s *SettingsService) ClearConnectionAddress() error {
	s.log.Info("clearing connection address")
	return models.DeleteSetting(s.db, SettingConnectionAddress)
}

func (s *SettingsService) GetPortRangeStart() int {
	v, err := models.GetSetting(s.db, SettingPortRangeStart)
	if err != nil || v == "" {
		return DefaultPortRangeStart
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultPortRangeStart
	}
	return n
}

func (s *SettingsService) GetPortRangeEnd() int {
	v, err := models.GetSetting(s.db, SettingPortRangeEnd)
	if err != nil || v == "" {
		return DefaultPortRangeEnd
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultPortRangeEnd
	}
	return n
}

func (s *SettingsService) GetPreferredPortMode() string {
	v, err := models.GetSetting(s.db, SettingPreferredPortMode)
	if err != nil || v == "" {
		return DefaultPreferredPortMode
	}
	if v != "auto" && v != "manual" {
		return DefaultPreferredPortMode
	}
	return v
}

func (s *SettingsService) SetPortRangeStart(v int) error {
	return models.SetSetting(s.db, SettingPortRangeStart, strconv.Itoa(v))
}

func (s *SettingsService) SetPortRangeEnd(v int) error {
	return models.SetSetting(s.db, SettingPortRangeEnd, strconv.Itoa(v))
}

func (s *SettingsService) SetPreferredPortMode(mode string) error {
	if mode != "auto" && mode != "manual" {
		mode = DefaultPreferredPortMode
	}
	return models.SetSetting(s.db, SettingPreferredPortMode, mode)
}

// GetMaxBackups returns the maximum number of backups to keep per gameserver.
// 0 means unlimited.
func (s *SettingsService) GetMaxBackups() int {
	v, err := models.GetSetting(s.db, SettingMaxBackups)
	if err != nil || v == "" {
		return DefaultMaxBackups
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultMaxBackups
	}
	return n
}

func (s *SettingsService) SetMaxBackups(v int) error {
	return models.SetSetting(s.db, SettingMaxBackups, strconv.Itoa(v))
}

// GetAuthEnabled returns true if auth is enabled.
// ENV var GJ_AUTH_ENABLED overrides DB setting.
func (s *SettingsService) GetAuthEnabled() bool {
	if v := os.Getenv("GJ_AUTH_ENABLED"); v != "" {
		return v == "true" || v == "1"
	}
	v, err := models.GetSetting(s.db, SettingAuthEnabled)
	if err != nil || v == "" {
		return false
	}
	return v == "true"
}

func (s *SettingsService) IsAuthEnabledFromEnv() bool {
	return os.Getenv("GJ_AUTH_ENABLED") != ""
}

func (s *SettingsService) SetAuthEnabled(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	s.log.Info("setting auth_enabled", "enabled", enabled)
	return models.SetSetting(s.db, SettingAuthEnabled, v)
}

// GetLocalhostBypass returns true if localhost requests bypass auth.
// ENV var GJ_LOCALHOST_BYPASS overrides DB setting. Defaults to true.
func (s *SettingsService) GetLocalhostBypass() bool {
	if v := os.Getenv("GJ_LOCALHOST_BYPASS"); v != "" {
		return v == "true" || v == "1"
	}
	v, err := models.GetSetting(s.db, SettingLocalhostBypass)
	if err != nil || v == "" {
		return true // default: bypass enabled
	}
	return v == "true"
}

func (s *SettingsService) IsLocalhostBypassFromEnv() bool {
	return os.Getenv("GJ_LOCALHOST_BYPASS") != ""
}

func (s *SettingsService) SetLocalhostBypass(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	s.log.Info("setting localhost_bypass", "enabled", enabled)
	return models.SetSetting(s.db, SettingLocalhostBypass, v)
}
