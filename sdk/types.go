package gamejanitor

import (
	"encoding/json"
	"time"
)

// Gameserver represents a game server instance.
type Gameserver struct {
	ID                string            `json:"id"`
	Name              string            `json:"name"`
	GameID            string            `json:"game_id"`
	Ports             []PortMapping     `json:"ports"`
	Env               map[string]string `json:"env"`
	MemoryLimitMB     int               `json:"memory_limit_mb"`
	CPULimit          float64           `json:"cpu_limit"`
	CPUEnforced       bool              `json:"cpu_enforced"`
	InstanceID       *string           `json:"instance_id"`
	VolumeName        string            `json:"volume_name"`
	Status            string            `json:"status"`
	ErrorReason       string            `json:"error_reason"`
	PortMode          string            `json:"port_mode"`
	NodeID            *string           `json:"node_id"`
	Node              *GameserverNode   `json:"node,omitempty"`
	SFTPUsername      string            `json:"sftp_username"`
	SFTPPort          int               `json:"sftp_port,omitempty"`
	Installed         bool              `json:"installed"`
	BackupLimit       *int              `json:"backup_limit"`
	StorageLimitMB    *int              `json:"storage_limit_mb"`
	NodeTags          map[string]string `json:"node_tags"`
	AutoRestart       *bool             `json:"auto_restart"`
	ConnectionAddress *string           `json:"connection_address"`
	ConnectionHost    string            `json:"connection_host,omitempty"`
	DesiredState      string            `json:"desired_state"`
	RestartRequired   bool              `json:"restart_required"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	Operation         *Operation        `json:"operation,omitempty"`
	Grants            map[string][]string `json:"grants,omitempty"`
	CreatedByTokenID  *string           `json:"created_by_token_id,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

// Operation describes an in-flight lifecycle operation on a gameserver
// (start, stop, backup, etc.). Nil when no operation is active.
type Operation struct {
	Type     string             `json:"type"`
	Phase    string             `json:"phase"`
	Progress *OperationProgress `json:"progress,omitempty"`
}

// OperationProgress carries progress details for long-running phases
// (depot download, image pull). Nil during phases that don't report progress.
type OperationProgress struct {
	Percent        float64 `json:"percent"`
	CompletedBytes uint64  `json:"completed_bytes,omitempty"`
	TotalBytes     uint64  `json:"total_bytes,omitempty"`
}

// GameserverNode contains the resolved node IPs for a gameserver.
type GameserverNode struct {
	ExternalIP string `json:"external_ip"`
	LanIP      string `json:"lan_ip"`
}

// PortMapping represents a single port binding on a gameserver.
type PortMapping struct {
	Name          string `json:"name"`
	HostPort      int    `json:"host_port"`
	InstancePort int    `json:"instance_port"`
	Protocol      string `json:"protocol"`
}

// CreateGameserverRequest is the request body for creating a gameserver.
type CreateGameserverRequest struct {
	Name              string            `json:"name"`
	GameID            string            `json:"game_id"`
	Ports             []PortMapping     `json:"ports,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	MemoryLimitMB     int               `json:"memory_limit_mb,omitempty"`
	CPULimit          float64           `json:"cpu_limit,omitempty"`
	CPUEnforced       bool              `json:"cpu_enforced,omitempty"`
	PortMode          string            `json:"port_mode,omitempty"`
	NodeID            *string           `json:"node_id,omitempty"`
	BackupLimit       *int              `json:"backup_limit,omitempty"`
	StorageLimitMB    *int              `json:"storage_limit_mb,omitempty"`
	NodeTags          map[string]string `json:"node_tags,omitempty"`
	AutoRestart       *bool             `json:"auto_restart,omitempty"`
	ConnectionAddress *string           `json:"connection_address,omitempty"`
}

// CreateGameserverResponse includes the gameserver and the one-time SFTP password.
type CreateGameserverResponse struct {
	Gameserver
	SFTPPassword string `json:"sftp_password"`
}

// UpdateGameserverRequest is the request body for updating a gameserver.
// Only set fields you want to change.
type UpdateGameserverRequest struct {
	Name              *string           `json:"name,omitempty"`
	Ports             []PortMapping     `json:"ports,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	MemoryLimitMB     *int              `json:"memory_limit_mb,omitempty"`
	CPULimit          *float64          `json:"cpu_limit,omitempty"`
	CPUEnforced       *bool             `json:"cpu_enforced,omitempty"`
	PortMode          *string           `json:"port_mode,omitempty"`
	NodeID            *string           `json:"node_id,omitempty"`
	BackupLimit       *int              `json:"backup_limit,omitempty"`
	StorageLimitMB    *int              `json:"storage_limit_mb,omitempty"`
	NodeTags          map[string]string `json:"node_tags,omitempty"`
	AutoRestart       *bool             `json:"auto_restart,omitempty"`
	ConnectionAddress *string           `json:"connection_address,omitempty"`
	Grants            map[string][]string `json:"grants,omitempty"`
}

// UpdateGameserverResponse is returned by update — it may include a migration flag.
type UpdateGameserverResponse struct {
	Gameserver
	MigrationTriggered bool `json:"migration_triggered,omitempty"`
}

// GameserverListResponse is the response from listing gameservers.
type GameserverListResponse struct {
	Gameservers []Gameserver `json:"gameservers"`
	Permissions []string     `json:"permissions"`
}

// BulkActionRequest triggers a lifecycle action on multiple gameservers.
type BulkActionRequest struct {
	Action string `json:"action"` // "start", "stop", or "restart"
	NodeID string `json:"node_id,omitempty"`
	All    bool   `json:"all,omitempty"`
}

// BulkActionResult is the outcome for a single gameserver in a bulk action.
type BulkActionResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// QueryData is the response from the query endpoint (live server state).
type QueryData struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

// GameserverStats is the response from the stats endpoint.
type GameserverStats struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsageMB   float64 `json:"memory_usage_mb"`
	MemoryLimitMB   float64 `json:"memory_limit_mb"`
	NetRxBytes      int64   `json:"net_rx_bytes"`
	NetTxBytes      int64   `json:"net_tx_bytes"`
	VolumeSizeBytes int64   `json:"volume_size_bytes"`
	StorageLimitMB  *int    `json:"storage_limit_mb,omitempty"`
}

// LogsResponse is the response from the logs endpoint.
type LogsResponse struct {
	Lines      []string `json:"lines"`
	Historical bool     `json:"historical,omitempty"`
}

// SendCommandRequest is the request body for sending a console command.
type SendCommandRequest struct {
	Command string `json:"command"`
}

// SendCommandResponse is the response from a console command.
type SendCommandResponse struct {
	Output string `json:"output"`
}

// RegenerateSFTPPasswordResponse contains the new SFTP password.
type RegenerateSFTPPasswordResponse struct {
	SFTPPassword string `json:"sftp_password"`
}

// MigrateRequest is the request body for migrating a gameserver.
type MigrateRequest struct {
	NodeID string `json:"node_id"`
}

// --- Backups ---

// Backup represents a gameserver backup.
type Backup struct {
	ID           string    `json:"id"`
	GameserverID string    `json:"gameserver_id"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"size_bytes"`
	Status       string    `json:"status"`
	ErrorReason  string    `json:"error_reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateBackupRequest is the optional request body for creating a backup.
type CreateBackupRequest struct {
	Name string `json:"name,omitempty"`
}

// --- Schedules ---

// Schedule represents a scheduled task on a gameserver.
type Schedule struct {
	ID           string          `json:"id"`
	GameserverID string          `json:"gameserver_id"`
	Name         string          `json:"name"`
	Type         string          `json:"type"`
	CronExpr     string          `json:"cron_expr"`
	Payload      json.RawMessage `json:"payload"`
	Enabled      bool            `json:"enabled"`
	OneShot      bool            `json:"one_shot"`
	LastRun      *time.Time      `json:"last_run"`
	NextRun      *time.Time      `json:"next_run"`
	CreatedAt    time.Time       `json:"created_at"`
}

// CreateScheduleRequest is the request body for creating a schedule.
type CreateScheduleRequest struct {
	Name     string          `json:"name"`
	Type     string          `json:"type"`
	CronExpr string          `json:"cron_expr"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Enabled  *bool           `json:"enabled,omitempty"`
	OneShot  bool            `json:"one_shot,omitempty"`
}

// UpdateScheduleRequest is the request body for updating a schedule.
type UpdateScheduleRequest struct {
	Name     *string          `json:"name,omitempty"`
	Type     *string          `json:"type,omitempty"`
	CronExpr *string          `json:"cron_expr,omitempty"`
	Payload  json.RawMessage  `json:"payload,omitempty"`
	Enabled  *bool            `json:"enabled,omitempty"`
	OneShot  *bool            `json:"one_shot,omitempty"`
}

// --- Workers ---

// Worker represents a worker node in the cluster.
type Worker struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	LanIP              string            `json:"lan_ip"`
	ExternalIP         string            `json:"external_ip"`
	CPUCores           int64             `json:"cpu_cores"`
	MemoryTotalMB      int64             `json:"memory_total_mb"`
	MemoryAvailableMB  int64             `json:"memory_available_mb"`
	GameserverCount    int               `json:"gameserver_count"`
	AllocatedMemoryMB  int               `json:"allocated_memory_mb"`
	AllocatedCPU       float64           `json:"allocated_cpu"`
	AllocatedStorageMB int               `json:"allocated_storage_mb"`
	DiskTotalMB        int64             `json:"disk_total_mb"`
	DiskAvailableMB    int64             `json:"disk_available_mb"`
	MaxMemoryMB        *int              `json:"max_memory_mb"`
	MaxCPU             *float64          `json:"max_cpu"`
	MaxStorageMB       *int              `json:"max_storage_mb"`
	Cordoned           bool              `json:"cordoned"`
	Tags               map[string]string `json:"tags"`
	Status             string            `json:"status"`
	LastSeen           *string           `json:"last_seen"`
}

// UpdateWorkerRequest is the request body for updating a worker node.
type UpdateWorkerRequest struct {
	Name         *string           `json:"name,omitempty"`
	MaxMemoryMB  *int              `json:"max_memory_mb,omitempty"`
	MaxCPU       *float64          `json:"max_cpu,omitempty"`
	MaxStorageMB *int              `json:"max_storage_mb,omitempty"`
	Cordoned     *bool             `json:"cordoned,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// --- Tokens ---

// Token represents an API token (hashed value is never returned).
type Token struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Role           string     `json:"role"`
	CanCreate      bool       `json:"can_create"`
	MaxGameservers *int       `json:"max_gameservers,omitempty"`
	MaxMemoryMB    *int       `json:"max_memory_mb,omitempty"`
	MaxCPU         *float64   `json:"max_cpu,omitempty"`
	MaxStorageMB   *int       `json:"max_storage_mb,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

// CreateTokenRequest is the request body for creating an API token.
type CreateTokenRequest struct {
	Name           string   `json:"name"`
	Role           string   `json:"role"` // "admin", "user", or "worker"
	CanCreate      bool     `json:"can_create,omitempty"`
	ExpiresIn      string   `json:"expires_in,omitempty"` // Go duration string, e.g. "720h"
	MaxGameservers *int     `json:"max_gameservers,omitempty"`
	MaxMemoryMB    *int                `json:"max_memory_mb,omitempty"`
	MaxCPU         *float64            `json:"max_cpu,omitempty"`
	MaxStorageMB   *int                `json:"max_storage_mb,omitempty"`
}

// CreateTokenResponse is returned when a token is created.
type CreateTokenResponse struct {
	Token   string `json:"token"`
	TokenID string `json:"token_id"`
	Name    string `json:"name"`
}

// --- Webhooks ---

// WebhookEndpoint represents a webhook configuration.
type WebhookEndpoint struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	SecretSet   bool      `json:"secret_set"`
	Events      []string  `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// CreateWebhookRequest is the request body for creating a webhook.
type CreateWebhookRequest struct {
	Description string   `json:"description"`
	URL         string   `json:"url"`
	Secret      string   `json:"secret,omitempty"`
	Events      []string `json:"events"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

// UpdateWebhookRequest is the request body for updating a webhook.
type UpdateWebhookRequest struct {
	Description *string  `json:"description,omitempty"`
	URL         *string  `json:"url,omitempty"`
	Secret      *string  `json:"secret,omitempty"`
	Events      []string `json:"events,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
}

// WebhookDelivery represents a single webhook delivery attempt.
type WebhookDelivery struct {
	ID            string     `json:"id"`
	EventType     string     `json:"event_type"`
	State         string     `json:"state"`
	Attempts      int        `json:"attempts"`
	LastAttemptAt *time.Time `json:"last_attempt_at"`
	NextAttemptAt time.Time  `json:"next_attempt_at"`
	LastError     string     `json:"last_error,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// --- Events ---

// Event represents a persisted event in the events system.
type Event struct {
	ID           string          `json:"id"`
	GameserverID *string         `json:"gameserver_id,omitempty"`
	WorkerID     string          `json:"worker_id,omitempty"`
	Type         string          `json:"type"`
	Actor        json.RawMessage `json:"actor"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
}

// --- Mods ---

// ModTabConfig is the full mods tab configuration.
type ModTabConfig struct {
	Version    *VersionPickerConfig `json:"version,omitempty"`
	Loader     *LoaderPickerConfig  `json:"loader,omitempty"`
	Categories []ModCategoryDef     `json:"categories"`
}

type VersionPickerConfig struct {
	Env     string          `json:"env"`
	Current string          `json:"current"`
	Options []DynamicOption `json:"options"`
}

type LoaderOption struct {
	Value      string   `json:"value"`
	ModSources []string `json:"mod_sources"`
}

type LoaderPickerConfig struct {
	Env     string         `json:"env"`
	Current string         `json:"current"`
	Options []LoaderOption `json:"options"`
}

type DynamicOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Group string `json:"group,omitempty"`
}

type ModCategoryDef struct {
	Name            string             `json:"name"`
	AlwaysAvailable bool               `json:"always_available,omitempty"`
	Sources         []ModCategorySource `json:"sources"`
}

type ModCategorySource struct {
	Name          string            `json:"name"`
	Delivery      string            `json:"delivery"`
	InstallPath   string            `json:"install_path,omitempty"`
	OverridesPath string            `json:"overrides_path,omitempty"`
	Filters       map[string]string `json:"filters,omitempty"`
	Config        map[string]string `json:"config,omitempty"`
}

type InstalledMod struct {
	ID            string          `json:"id"`
	GameserverID  string          `json:"gameserver_id"`
	Source        string          `json:"source"`
	SourceID      string          `json:"source_id"`
	Category      string          `json:"category"`
	Name          string          `json:"name"`
	Version       string          `json:"version"`
	VersionID     string          `json:"version_id"`
	FilePath    string `json:"file_path"`
	FileName    string `json:"file_name"`
	DownloadURL string `json:"download_url"`
	FileHash    string `json:"file_hash"`
	Delivery    string `json:"delivery"`
	AutoInstalled bool            `json:"auto_installed"`
	DependsOn     *string         `json:"depends_on,omitempty"`
	PackID        *string         `json:"pack_id,omitempty"`
	Metadata      json.RawMessage `json:"metadata"`
	InstalledAt   time.Time       `json:"installed_at"`
}

type ModSearchResult struct {
	SourceID    string `json:"source_id"`
	Source      string `json:"source"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	Author      string `json:"author"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Downloads   int    `json:"downloads"`
	UpdatedAt   string `json:"updated_at"`
}

type ModVersion struct {
	VersionID    string   `json:"version_id"`
	Version      string   `json:"version"`
	FileName     string   `json:"file_name"`
	DownloadURL  string   `json:"download_url"`
	GameVersion  string   `json:"game_version"`
	GameVersions []string `json:"game_versions,omitempty"`
	Loader       string   `json:"loader"`
}

type ModUpdate struct {
	ModID          string     `json:"mod_id"`
	ModName        string     `json:"mod_name"`
	CurrentVersion string     `json:"current_version"`
	LatestVersion  ModVersion `json:"latest_version"`
}

type ModIssue struct {
	ModID   string `json:"mod_id"`
	ModName string `json:"mod_name"`
	Type    string `json:"type"`
	Reason  string `json:"reason"`
}

type InstallModRequest struct {
	Category  string `json:"category"`
	Source    string `json:"source"`
	SourceID  string `json:"source_id"`
	VersionID string `json:"version_id,omitempty"`
}

type InstallPackRequest struct {
	Source    string `json:"source"`
	PackID    string `json:"pack_id"`
	VersionID string `json:"version_id,omitempty"`
}

type SearchResults struct {
	Results []ModSearchResult `json:"results"`
	Total   int               `json:"total"`
	Offset  int               `json:"offset"`
	Limit   int               `json:"limit"`
}

type ScanResult struct {
	Tracked   []InstalledMod  `json:"tracked"`
	Untracked []UntrackedFile `json:"untracked"`
	Missing   []InstalledMod  `json:"missing"`
}

type UntrackedFile struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Category string `json:"category"`
}

type InstallURLRequest struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}

type TrackFileRequest struct {
	Category string `json:"category"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// --- Files ---

// FileEntry represents a file or directory in a gameserver's file system.
type FileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime string `json:"mod_time"`
}

// RenameFileRequest is the request body for renaming a file.
type RenameFileRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// CreateDirectoryRequest is the request body for creating a directory.
type CreateDirectoryRequest struct {
	Path string `json:"path"`
}

// FileContent is the response from reading a file.
type FileContent struct {
	Content string `json:"content"`
}

// --- Games (metadata) ---

// Game represents a game definition (available game type).
type Game struct {
	ID                   string          `json:"id"`
	Name                 string          `json:"name"`
	Aliases              []string        `json:"aliases,omitempty"`
	Description          string          `json:"description,omitempty"`
	BaseImage            string          `json:"base_image"`
	IconPath             string          `json:"icon_path"`
	DefaultPorts         []GamePort      `json:"default_ports"`
	DefaultEnv           []GameEnvVar    `json:"default_env"`
	RecommendedMemoryMB  int             `json:"recommended_memory_mb"`
	ReadyPattern         string          `json:"ready_pattern,omitempty"`
	DisabledCapabilities []string        `json:"disabled_capabilities"`
	Mods                 json.RawMessage `json:"mods,omitempty"`
}

// GamePort is a default port definition for a game.
type GamePort struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// GameEnvVar is an environment variable definition for a game.
type GameEnvVar struct {
	Key             string          `json:"key"`
	Default         string          `json:"default"`
	Label           string          `json:"label,omitempty"`
	Type            string          `json:"type,omitempty"`
	Group           string          `json:"group,omitempty"`
	Options         []string        `json:"options,omitempty"`
	DynamicOptions  json.RawMessage `json:"dynamic_options,omitempty"`
	Required        bool            `json:"required,omitempty"`
	ConsentRequired bool            `json:"consent_required,omitempty"`
	Notice          string          `json:"notice,omitempty"`
	Autogenerate    string          `json:"autogenerate,omitempty"`
	System          bool            `json:"system,omitempty"`
	Hidden          bool            `json:"hidden,omitempty"`
	TriggersInstall bool            `json:"triggers_install,omitempty"`
}

// GameOption represents a dynamic option value for a game's env var.
type GameOption json.RawMessage

// --- Settings ---

// Settings is a map of setting key to value. Keys and value types depend on
// the Gamejanitor server configuration.
type Settings map[string]json.RawMessage

// --- Status ---

// ClusterStatusResponse is the response from the status endpoint.
type ClusterStatusResponse struct {
	Config      ConfigStatus      `json:"config"`
	Cluster     ClusterStatus     `json:"cluster"`
	Gameservers GameserverCounts  `json:"gameservers"`
}

// ConfigStatus describes the server's runtime configuration.
type ConfigStatus struct {
	Bind             string `json:"bind"`
	Port             int    `json:"port"`
	GRPCPort         int    `json:"grpc_port"`
	SFTPPort         int    `json:"sftp_port"`
	DataDir          string `json:"data_dir"`
	Runtime string `json:"runtime"`
	BackupStoreType  string `json:"backup_store_type"`
	WebUI            bool   `json:"web_ui"`
	Controller       bool   `json:"controller"`
	Worker           bool   `json:"worker"`
}

// ClusterStatus describes the cluster's resource allocation.
type ClusterStatus struct {
	Workers            int     `json:"workers"`
	WorkersCordoned    int     `json:"workers_cordoned"`
	TotalMemoryMB      int64   `json:"total_memory_mb"`
	AllocatedMemoryMB  int     `json:"allocated_memory_mb"`
	TotalCPU           float64 `json:"total_cpu"`
	AllocatedCPU       float64 `json:"allocated_cpu"`
	TotalStorageMB     int64   `json:"total_storage_mb"`
	AllocatedStorageMB int     `json:"allocated_storage_mb"`
}

// GameserverCounts is a summary of gameserver states.
type GameserverCounts struct {
	Total      int `json:"total"`
	Running    int `json:"running"`
	Stopped    int `json:"stopped"`
	Installing int `json:"installing"`
	Error      int `json:"error"`
}

// --- Logs ---

// AppLogs is the response from the application logs endpoint.
type AppLogs struct {
	Lines []string `json:"lines"`
}

// Ptr returns a pointer to v. Useful for setting optional fields on request structs.
func Ptr[T any](v T) *T {
	return &v
}
