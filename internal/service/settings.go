package service

import (
	"database/sql"
	"log/slog"
	"os"
	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

// ResolveConnectionIP returns the connection IP for a gameserver on the given node.
// Priority: global override > worker's persisted IP > empty (caller falls back to 127.0.0.1).
func (s *SettingsService) ResolveConnectionIP(nodeID *string) (ip string, configured bool) {
	if globalIP := s.GetConnectionAddress(); globalIP != "" {
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

// GetWorkerNode returns a single worker node by ID.
func (s *SettingsService) GetWorkerNode(id string) (*models.WorkerNode, error) {
	return models.GetWorkerNode(s.db, id)
}

// SetWorkerNodePortRange updates the port range for a specific worker node.
func (s *SettingsService) SetWorkerNodePortRange(id string, start, end *int) error {
	return models.SetWorkerNodePortRange(s.db, id, start, end)
}

// SetWorkerNodeLimits updates the resource limits for a specific worker node.
func (s *SettingsService) SetWorkerNodeLimits(id string, maxMemoryMB, maxGameservers *int) error {
	return models.SetWorkerNodeLimits(s.db, id, maxMemoryMB, maxGameservers)
}

// ListGameserversByNode returns all gameservers for computing per-node resource usage.
func (s *SettingsService) ListGameserversByNode() ([]models.Gameserver, error) {
	return models.ListGameservers(s.db, models.GameserverFilter{})
}

const (
	SettingConnectionAddress = "connection_address"
	SettingPortRangeStart    = "port_range_start"
	SettingPortRangeEnd      = "port_range_end"
	SettingPreferredPortMode = "preferred_port_mode"
	SettingMaxBackups        = "max_backups"
	SettingAuthEnabled       = "auth_enabled"
	SettingLocalhostBypass   = "localhost_bypass"
	SettingAuditRetention    = "audit_retention_days"
	SettingRateLimitEnabled  = "rate_limit_enabled"
	SettingRateLimitPerIP    = "rate_limit_per_ip"
	SettingRateLimitPerToken = "rate_limit_per_token"
	SettingRateLimitLogin    = "rate_limit_login"
	SettingTrustProxyHeaders = "trust_proxy_headers"

	DefaultAuditRetention = 30

	DefaultPortRangeStart    = 27000
	DefaultPortRangeEnd      = 28999
	DefaultPreferredPortMode = "auto"
	DefaultMaxBackups        = 10

	DefaultRateLimitPerIP    = 20
	DefaultRateLimitPerToken = 10
	DefaultRateLimitLogin    = 10
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

// GetPortRangeStart returns the start of the port allocation range.
// Priority: ENV var > DB setting > default.
func (s *SettingsService) GetPortRangeStart() int {
	if v := os.Getenv("GJ_PORT_RANGE_START"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
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

// GetPortRangeEnd returns the end of the port allocation range.
// Priority: ENV var > DB setting > default.
func (s *SettingsService) GetPortRangeEnd() int {
	if v := os.Getenv("GJ_PORT_RANGE_END"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
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

func (s *SettingsService) IsPortRangeFromEnv() bool {
	return os.Getenv("GJ_PORT_RANGE_START") != "" || os.Getenv("GJ_PORT_RANGE_END") != ""
}

// GetPreferredPortMode returns the preferred port allocation mode.
// Priority: ENV var > DB setting > default.
func (s *SettingsService) GetPreferredPortMode() string {
	if v := os.Getenv("GJ_PORT_MODE"); v != "" {
		if v == "auto" || v == "manual" {
			return v
		}
	}
	v, err := models.GetSetting(s.db, SettingPreferredPortMode)
	if err != nil || v == "" {
		return DefaultPreferredPortMode
	}
	if v != "auto" && v != "manual" {
		return DefaultPreferredPortMode
	}
	return v
}

func (s *SettingsService) IsPortModeFromEnv() bool {
	return os.Getenv("GJ_PORT_MODE") != ""
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
// 0 means unlimited. Priority: ENV var > DB setting > default.
func (s *SettingsService) GetMaxBackups() int {
	if v := os.Getenv("GJ_MAX_BACKUPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
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

func (s *SettingsService) IsMaxBackupsFromEnv() bool {
	return os.Getenv("GJ_MAX_BACKUPS") != ""
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

func (s *SettingsService) GetAuditRetentionDays() int {
	if v := os.Getenv("GJ_AUDIT_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	v, err := models.GetSetting(s.db, SettingAuditRetention)
	if err != nil || v == "" {
		return DefaultAuditRetention
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultAuditRetention
	}
	return n
}

func (s *SettingsService) IsAuditRetentionFromEnv() bool {
	return os.Getenv("GJ_AUDIT_RETENTION_DAYS") != ""
}

func (s *SettingsService) SetAuditRetentionDays(v int) error {
	return models.SetSetting(s.db, SettingAuditRetention, strconv.Itoa(v))
}

func (s *SettingsService) GetRateLimitEnabled() bool {
	if v := os.Getenv("GJ_RATE_LIMIT_ENABLED"); v != "" {
		return v == "true" || v == "1"
	}
	v, err := models.GetSetting(s.db, SettingRateLimitEnabled)
	if err != nil || v == "" {
		return false
	}
	return v == "true"
}

func (s *SettingsService) IsRateLimitEnabledFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_ENABLED") != ""
}

func (s *SettingsService) SetRateLimitEnabled(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	s.log.Info("setting rate_limit_enabled", "enabled", enabled)
	return models.SetSetting(s.db, SettingRateLimitEnabled, v)
}

func (s *SettingsService) GetRateLimitPerIP() int {
	if v := os.Getenv("GJ_RATE_LIMIT_PER_IP"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	v, err := models.GetSetting(s.db, SettingRateLimitPerIP)
	if err != nil || v == "" {
		return DefaultRateLimitPerIP
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultRateLimitPerIP
	}
	return n
}

func (s *SettingsService) IsRateLimitPerIPFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_PER_IP") != ""
}

func (s *SettingsService) SetRateLimitPerIP(v int) error {
	return models.SetSetting(s.db, SettingRateLimitPerIP, strconv.Itoa(v))
}

func (s *SettingsService) GetRateLimitPerToken() int {
	if v := os.Getenv("GJ_RATE_LIMIT_PER_TOKEN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	v, err := models.GetSetting(s.db, SettingRateLimitPerToken)
	if err != nil || v == "" {
		return DefaultRateLimitPerToken
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultRateLimitPerToken
	}
	return n
}

func (s *SettingsService) IsRateLimitPerTokenFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_PER_TOKEN") != ""
}

func (s *SettingsService) SetRateLimitPerToken(v int) error {
	return models.SetSetting(s.db, SettingRateLimitPerToken, strconv.Itoa(v))
}

func (s *SettingsService) GetRateLimitLogin() int {
	if v := os.Getenv("GJ_RATE_LIMIT_LOGIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	v, err := models.GetSetting(s.db, SettingRateLimitLogin)
	if err != nil || v == "" {
		return DefaultRateLimitLogin
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return DefaultRateLimitLogin
	}
	return n
}

func (s *SettingsService) IsRateLimitLoginFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_LOGIN") != ""
}

func (s *SettingsService) SetRateLimitLogin(v int) error {
	return models.SetSetting(s.db, SettingRateLimitLogin, strconv.Itoa(v))
}

func (s *SettingsService) GetTrustProxyHeaders() bool {
	if v := os.Getenv("GJ_TRUST_PROXY_HEADERS"); v != "" {
		return v == "true" || v == "1"
	}
	v, err := models.GetSetting(s.db, SettingTrustProxyHeaders)
	if err != nil || v == "" {
		return false
	}
	return v == "true"
}

func (s *SettingsService) IsTrustProxyHeadersFromEnv() bool {
	return os.Getenv("GJ_TRUST_PROXY_HEADERS") != ""
}

func (s *SettingsService) SetTrustProxyHeaders(enabled bool) error {
	v := "false"
	if enabled {
		v = "true"
	}
	s.log.Info("setting trust_proxy_headers", "enabled", enabled)
	return models.SetSetting(s.db, SettingTrustProxyHeaders, v)
}
