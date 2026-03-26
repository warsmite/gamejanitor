package model

import (
	"encoding/json"
	"time"
)

// Operation status constants
const (
	OperationStatusRunning   = "running"
	OperationStatusCompleted = "completed"
	OperationStatusFailed    = "failed"
	OperationStatusAbandoned = "abandoned"
)

// Operation type constants — every stateful dispatch to a worker.
const (
	OpStart     = "start"
	OpStop      = "stop"
	OpRestart   = "restart"
	OpUpdate    = "update"
	OpReinstall = "reinstall"
	OpMigrate   = "migrate"
	OpBackup    = "backup"
	OpRestore   = "restore"
)

type Operation struct {
	ID           string          `json:"id"`
	GameserverID string          `json:"gameserver_id"`
	WorkerID     string          `json:"worker_id"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	Error        string          `json:"error,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type OperationFilter struct {
	GameserverID *string
	Status       *string
	WorkerID     *string
}
