package service

import (
	"database/sql"
	"log/slog"
	"os"
	"strconv"

	"github.com/warsmite/gamejanitor/internal/models"
)

// ResolveConnectionIP returns the connection IP for a gameserver on the given node.
// Priority: global override > worker external IP > worker LAN IP > empty (caller falls back to 127.0.0.1).
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
	SettingTrustProxyHeaders    = "trust_proxy_headers"
	SettingEventRetention      = "event_retention_days"
	SettingRequireMemoryLimit  = "require_memory_limit"
	SettingRequireCPULimit     = "require_cpu_limit"
	SettingRequireStorageLimit = "require_storage_limit"

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

func (s *SettingsService) getInt(envKey, dbKey string, defaultVal int) int {
	if v := os.Getenv(envKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	v, err := models.GetSetting(s.db, dbKey)
	if err != nil || v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return defaultVal
	}
	return n
}

func (s *SettingsService) getBool(envKey, dbKey string, defaultVal bool) bool {
	if v := os.Getenv(envKey); v != "" {
		return v == "true" || v == "1"
	}
	v, err := models.GetSetting(s.db, dbKey)
	if err != nil || v == "" {
		return defaultVal
	}
	return v == "true"
}

func (s *SettingsService) getString(envKey, dbKey string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	v, err := models.GetSetting(s.db, dbKey)
	if err != nil {
		s.log.Error("reading setting", "key", dbKey, "error", err)
		return ""
	}
	return v
}

func (s *SettingsService) setInt(dbKey string, v int) error {
	return models.SetSetting(s.db, dbKey, strconv.Itoa(v))
}

func (s *SettingsService) setBool(dbKey string, v bool) error {
	val := "false"
	if v {
		val = "true"
	}
	return models.SetSetting(s.db, dbKey, val)
}

// --- Connection Address ---

func (s *SettingsService) GetConnectionAddress() string {
	return s.getString("GJ_CONNECTION_ADDRESS", SettingConnectionAddress)
}

func (s *SettingsService) IsConnectionAddressConfigured() bool {
	return s.GetConnectionAddress() != ""
}

func (s *SettingsService) IsConnectionAddressFromEnv() bool {
	return os.Getenv("GJ_CONNECTION_ADDRESS") != ""
}

func (s *SettingsService) SetConnectionAddress(address string) error {
	return models.SetSetting(s.db, SettingConnectionAddress, address)
}

func (s *SettingsService) ClearConnectionAddress() error {
	return models.DeleteSetting(s.db, SettingConnectionAddress)
}

// --- Port Range ---

func (s *SettingsService) GetPortRangeStart() int {
	return s.getInt("GJ_PORT_RANGE_START", SettingPortRangeStart, DefaultPortRangeStart)
}

func (s *SettingsService) GetPortRangeEnd() int {
	return s.getInt("GJ_PORT_RANGE_END", SettingPortRangeEnd, DefaultPortRangeEnd)
}

func (s *SettingsService) IsPortRangeFromEnv() bool {
	return os.Getenv("GJ_PORT_RANGE_START") != "" || os.Getenv("GJ_PORT_RANGE_END") != ""
}

func (s *SettingsService) SetPortRangeStart(v int) error {
	return s.setInt(SettingPortRangeStart, v)
}

func (s *SettingsService) SetPortRangeEnd(v int) error {
	return s.setInt(SettingPortRangeEnd, v)
}

// --- Port Mode ---

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

func (s *SettingsService) SetPreferredPortMode(mode string) error {
	if mode != "auto" && mode != "manual" {
		mode = DefaultPreferredPortMode
	}
	return models.SetSetting(s.db, SettingPreferredPortMode, mode)
}

// --- Max Backups ---

func (s *SettingsService) GetMaxBackups() int {
	return s.getInt("GJ_MAX_BACKUPS", SettingMaxBackups, DefaultMaxBackups)
}

func (s *SettingsService) IsMaxBackupsFromEnv() bool {
	return os.Getenv("GJ_MAX_BACKUPS") != ""
}

func (s *SettingsService) SetMaxBackups(v int) error {
	return s.setInt(SettingMaxBackups, v)
}

// --- Auth ---

func (s *SettingsService) GetAuthEnabled() bool {
	return s.getBool("GJ_AUTH_ENABLED", SettingAuthEnabled, false)
}

func (s *SettingsService) IsAuthEnabledFromEnv() bool {
	return os.Getenv("GJ_AUTH_ENABLED") != ""
}

func (s *SettingsService) SetAuthEnabled(enabled bool) error {
	return s.setBool(SettingAuthEnabled, enabled)
}

// --- Localhost Bypass (defaults to true) ---

func (s *SettingsService) GetLocalhostBypass() bool {
	return s.getBool("GJ_LOCALHOST_BYPASS", SettingLocalhostBypass, true)
}

func (s *SettingsService) IsLocalhostBypassFromEnv() bool {
	return os.Getenv("GJ_LOCALHOST_BYPASS") != ""
}

func (s *SettingsService) SetLocalhostBypass(enabled bool) error {
	return s.setBool(SettingLocalhostBypass, enabled)
}

// --- Audit Retention ---

func (s *SettingsService) GetAuditRetentionDays() int {
	return s.getInt("GJ_AUDIT_RETENTION_DAYS", SettingAuditRetention, DefaultAuditRetention)
}

func (s *SettingsService) IsAuditRetentionFromEnv() bool {
	return os.Getenv("GJ_AUDIT_RETENTION_DAYS") != ""
}

func (s *SettingsService) SetAuditRetentionDays(v int) error {
	return s.setInt(SettingAuditRetention, v)
}

// --- Rate Limiting ---

func (s *SettingsService) GetRateLimitEnabled() bool {
	return s.getBool("GJ_RATE_LIMIT_ENABLED", SettingRateLimitEnabled, false)
}

func (s *SettingsService) IsRateLimitEnabledFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_ENABLED") != ""
}

func (s *SettingsService) SetRateLimitEnabled(enabled bool) error {
	return s.setBool(SettingRateLimitEnabled, enabled)
}

func (s *SettingsService) GetRateLimitPerIP() int {
	return s.getInt("GJ_RATE_LIMIT_PER_IP", SettingRateLimitPerIP, DefaultRateLimitPerIP)
}

func (s *SettingsService) IsRateLimitPerIPFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_PER_IP") != ""
}

func (s *SettingsService) SetRateLimitPerIP(v int) error {
	return s.setInt(SettingRateLimitPerIP, v)
}

func (s *SettingsService) GetRateLimitPerToken() int {
	return s.getInt("GJ_RATE_LIMIT_PER_TOKEN", SettingRateLimitPerToken, DefaultRateLimitPerToken)
}

func (s *SettingsService) IsRateLimitPerTokenFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_PER_TOKEN") != ""
}

func (s *SettingsService) SetRateLimitPerToken(v int) error {
	return s.setInt(SettingRateLimitPerToken, v)
}

func (s *SettingsService) GetRateLimitLogin() int {
	return s.getInt("GJ_RATE_LIMIT_LOGIN", SettingRateLimitLogin, DefaultRateLimitLogin)
}

func (s *SettingsService) IsRateLimitLoginFromEnv() bool {
	return os.Getenv("GJ_RATE_LIMIT_LOGIN") != ""
}

func (s *SettingsService) SetRateLimitLogin(v int) error {
	return s.setInt(SettingRateLimitLogin, v)
}

// --- Trust Proxy Headers ---

func (s *SettingsService) GetTrustProxyHeaders() bool {
	return s.getBool("GJ_TRUST_PROXY_HEADERS", SettingTrustProxyHeaders, false)
}

func (s *SettingsService) IsTrustProxyHeadersFromEnv() bool {
	return os.Getenv("GJ_TRUST_PROXY_HEADERS") != ""
}

func (s *SettingsService) SetTrustProxyHeaders(enabled bool) error {
	return s.setBool(SettingTrustProxyHeaders, enabled)
}

// --- Event Retention ---

func (s *SettingsService) GetEventRetentionDays() int {
	return s.getInt("GJ_EVENT_RETENTION_DAYS", SettingEventRetention, 30)
}

func (s *SettingsService) IsEventRetentionFromEnv() bool {
	return os.Getenv("GJ_EVENT_RETENTION_DAYS") != ""
}

func (s *SettingsService) SetEventRetentionDays(v int) error {
	return s.setInt(SettingEventRetention, v)
}

// --- Require Resource Limits ---

func (s *SettingsService) GetRequireMemoryLimit() bool {
	return s.getBool("GJ_REQUIRE_MEMORY_LIMIT", SettingRequireMemoryLimit, false)
}

func (s *SettingsService) IsRequireMemoryLimitFromEnv() bool {
	return os.Getenv("GJ_REQUIRE_MEMORY_LIMIT") != ""
}

func (s *SettingsService) SetRequireMemoryLimit(enabled bool) error {
	return s.setBool(SettingRequireMemoryLimit, enabled)
}

func (s *SettingsService) GetRequireCPULimit() bool {
	return s.getBool("GJ_REQUIRE_CPU_LIMIT", SettingRequireCPULimit, false)
}

func (s *SettingsService) IsRequireCPULimitFromEnv() bool {
	return os.Getenv("GJ_REQUIRE_CPU_LIMIT") != ""
}

func (s *SettingsService) SetRequireCPULimit(enabled bool) error {
	return s.setBool(SettingRequireCPULimit, enabled)
}

func (s *SettingsService) GetRequireStorageLimit() bool {
	return s.getBool("GJ_REQUIRE_STORAGE_LIMIT", SettingRequireStorageLimit, false)
}

func (s *SettingsService) IsRequireStorageLimitFromEnv() bool {
	return os.Getenv("GJ_REQUIRE_STORAGE_LIMIT") != ""
}

func (s *SettingsService) SetRequireStorageLimit(enabled bool) error {
	return s.setBool(SettingRequireStorageLimit, enabled)
}

