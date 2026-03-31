package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Container user identity — game processes run as this UID/GID inside containers.
const (
	GameserverUID  = 1001
	GameserverGID  = 1001
	GameserverPerm = 0644
)

type GameserverNode struct {
	ExternalIP string `json:"external_ip"`
	LanIP      string `json:"lan_ip"`
}

type Gameserver struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	GameID        string          `json:"game_id"`
	Ports         Ports           `json:"ports"`
	Env           Env             `json:"env"`
	MemoryLimitMB  int             `json:"memory_limit_mb"`
	CPULimit       float64         `json:"cpu_limit"`
	CPUEnforced    bool            `json:"cpu_enforced"`
	InstanceID    *string         `json:"instance_id"`
	VolumeName     string          `json:"volume_name"`
	Status         string          `json:"status"`
	ErrorReason    string          `json:"error_reason"`
	Operation      *Operation      `json:"operation,omitempty"`
	PortMode       string          `json:"port_mode"`
	NodeID         *string         `json:"node_id"`
	Node           *GameserverNode `json:"node,omitempty"`
	SFTPUsername   string          `json:"sftp_username"`
	HashedSFTPPassword string      `json:"-"`
	Installed      bool            `json:"installed"`
	BackupLimit    *int            `json:"backup_limit"`
	StorageLimitMB *int            `json:"storage_limit_mb"`
	NodeTags       Labels          `json:"node_tags"`
	AutoRestart        *bool           `json:"auto_restart"`
	ConnectionAddress  *string         `json:"connection_address"`
	AppliedConfig      *AppliedConfig  `json:"applied_config,omitempty"`
	Archived           bool            `json:"archived"`
	OperationType      *string         `json:"operation_type"`   // current running operation (start, stop, backup, etc.), nil when idle
	OperationID        *string         `json:"operation_id"`     // event ID of the operation start
	RestartRequired    bool            `json:"restart_required"` // derived, not stored
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// AppliedConfig captures the configuration that was used when the container was
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
	Name          string  `json:"name"`
	HostPort      FlexInt `json:"host_port"`
	ContainerPort FlexInt `json:"container_port"`
	Protocol      string  `json:"protocol"`
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
	GameID *string
	Status *string
	NodeID *string
	IDs    []string // restrict results to these IDs (used for scoped token filtering)
	Pagination
}
