package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Instance user identity — game processes run as this UID/GID inside instances.
const (
	GameserverUID  = 1001
	GameserverGID  = 1001
	GameserverPerm = 0644
)

// DesiredState captures the user's intent for a gameserver. It is persisted
// and is the durable input to lifecycle decisions. Observed reality (whether a
// process is actually running) is a separate, orthogonal concept — see
// ProcessState.
type DesiredState string

const (
	DesiredStopped  DesiredState = "stopped"
	DesiredRunning  DesiredState = "running"
	DesiredArchived DesiredState = "archived"
)

// ProcessState is the controller's view of a gameserver instance's lifecycle
// as reported by the worker. It is orthogonal to readiness — see Gameserver.Ready.
type ProcessState string

const (
	// ProcessNone means no worker instance exists for this gameserver.
	ProcessNone ProcessState = "none"
	// ProcessCreating is a transient state while the worker prepares the container.
	ProcessCreating ProcessState = "creating"
	// ProcessStarting means the process has been launched but has not yet been
	// observed as alive.
	ProcessStarting ProcessState = "starting"
	// ProcessRunning means the process is alive on the worker.
	ProcessRunning ProcessState = "running"
	// ProcessExited means the process has terminated. ExitCode carries the why.
	ProcessExited ProcessState = "exited"
)

type GameserverNode struct {
	ExternalIP string `json:"external_ip"`
	LanIP      string `json:"lan_ip"`
}

type Gameserver struct {
	// Identity
	ID         string `json:"id"`
	Name       string `json:"name"`
	GameID     string `json:"game_id"`
	VolumeName string `json:"volume_name"`

	// Spec — user intent, persisted
	Ports              Ports          `json:"ports"`
	Env                Env            `json:"env"`
	MemoryLimitMB      int            `json:"memory_limit_mb"`
	CPULimit           float64        `json:"cpu_limit"`
	CPUEnforced        bool           `json:"cpu_enforced"`
	PortMode           string         `json:"port_mode"`
	NodeID             *string        `json:"node_id"`
	BackupLimit        *int           `json:"backup_limit"`
	StorageLimitMB     *int           `json:"storage_limit_mb"`
	NodeTags           Labels         `json:"node_tags"`
	AutoRestart        *bool          `json:"auto_restart"`
	ConnectionAddress  *string        `json:"connection_address"`
	AppliedConfig      *AppliedConfig `json:"applied_config,omitempty"`
	DesiredState       DesiredState   `json:"desired_state"`
	CreatedByTokenID   *string        `json:"created_by_token_id,omitempty"`
	Grants             GrantMap       `json:"grants"`
	SFTPUsername       string         `json:"sftp_username"`
	HashedSFTPPassword string         `json:"-"`

	// Observed — runtime facts from the controller. Populated by Snapshot,
	// NOT scanned from the DB (InstanceID, Installed, ErrorReason are exceptions
	// that persist across controller restarts).
	InstanceID   *string      `json:"instance_id"`
	Installed    bool         `json:"installed"`
	ErrorReason  string       `json:"error_reason"`
	Operation    *Operation   `json:"operation,omitempty"`
	ProcessState ProcessState `json:"process_state"`
	Ready        bool         `json:"ready"`         // true when ready pattern matched (or no pattern)
	WorkerOnline bool         `json:"worker_online"` // true when the assigned worker is reachable
	ExitCode     int          `json:"exit_code,omitempty"`
	StartedAt    *time.Time   `json:"started_at,omitempty"`
	ReadyAt      *time.Time   `json:"ready_at,omitempty"`
	ExitedAt     *time.Time   `json:"exited_at,omitempty"`

	// Derived display helpers
	Node            *GameserverNode `json:"node,omitempty"`
	RestartRequired bool            `json:"restart_required"`
	ConnectionHost  string          `json:"connection_host,omitempty"`
	SFTPPort        int             `json:"sftp_port,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}


// AppliedConfig captures the configuration that was used when the instance was
// last created. Compared against current DB state to detect pending changes.
type AppliedConfig struct {
	Env           Env     `json:"env"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	CPULimit      float64 `json:"cpu_limit"`
	CPUEnforced   bool    `json:"cpu_enforced"`
}

func (ac *AppliedConfig) Scan(src any) error {
	if src == nil {
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("applied_config: unsupported scan type %T", src)
	}
	return json.Unmarshal(data, ac)
}

func (ac AppliedConfig) Value() (driver.Value, error) {
	return json.Marshal(ac)
}

// SnapshotConfig creates an AppliedConfig from the current gameserver state.
func (gs *Gameserver) SnapshotConfig() *AppliedConfig {
	return &AppliedConfig{
		Env:           gs.Env,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
		CPUEnforced:   gs.CPUEnforced,
	}
}

// ComputeRestartRequired sets RestartRequired by comparing the applied config
// against the current desired state. Only meaningful when the gameserver is running.
func (gs *Gameserver) ComputeRestartRequired() {
	if gs.AppliedConfig == nil || gs.InstanceID == nil {
		gs.RestartRequired = false
		return
	}
	ac := gs.AppliedConfig
	gs.RestartRequired = ac.MemoryLimitMB != gs.MemoryLimitMB ||
		ac.CPULimit != gs.CPULimit ||
		ac.CPUEnforced != gs.CPUEnforced ||
		!ac.Env.Equal(gs.Env)
}

// IsArchived returns true if the gameserver's desired state is archived.
func (gs *Gameserver) IsArchived() bool {
	return gs.DesiredState == DesiredArchived
}

// FlexInt handles JSON values that may be a number or a string containing a number.
// Used for port mappings where values come from user-provided JSON.
type FlexInt int

func (fi *FlexInt) UnmarshalJSON(b []byte) error {
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*fi = FlexInt(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("cannot unmarshal %s into int or string", string(b))
	}
	if s == "" {
		*fi = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("cannot parse %q as int: %w", s, err)
	}
	*fi = FlexInt(n)
	return nil
}

// PortMapping represents a single port binding stored in the gameserver's ports JSON.
type PortMapping struct {
	Name     string  `json:"name"`
	Port     FlexInt `json:"port"`
	Protocol string  `json:"protocol"`
}

// Ports is a slice of port mappings stored as JSON in the database.
type Ports []PortMapping

func (p *Ports) Scan(src any) error {
	if src == nil {
		*p = Ports{}
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("ports: unsupported scan type %T", src)
	}
	var parsed Ports
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("ports: invalid JSON %q: %w", string(data), err)
	}
	*p = parsed
	return nil
}

func (p Ports) Value() (driver.Value, error) {
	if p == nil {
		return "[]", nil
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("ports: marshal error: %w", err)
	}
	return string(data), nil
}

// Env is a key-value map of environment variables stored as JSON in the database.
type Env map[string]string

func (e Env) Equal(other Env) bool {
	if len(e) != len(other) {
		return false
	}
	for k, v := range e {
		if other[k] != v {
			return false
		}
	}
	return true
}

func (e *Env) Scan(src any) error {
	if src == nil {
		*e = Env{}
		return nil
	}
	var data []byte
	switch v := src.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		return fmt.Errorf("env: unsupported scan type %T", src)
	}
	parsed := Env{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("env: invalid JSON %q: %w", string(data), err)
	}
	*e = parsed
	return nil
}

func (e Env) Value() (driver.Value, error) {
	if e == nil {
		return "{}", nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("env: marshal error: %w", err)
	}
	return string(data), nil
}

type GameserverFilter struct {
	GameID       *string
	DesiredState *DesiredState
	NodeID       *string
	IDs          []string // restrict results to these IDs (used for scoped token filtering)
	Pagination
}
