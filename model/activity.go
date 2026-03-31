package model

import (
	"encoding/json"
	"time"
)

const (
	ActivityRunning   = "running"
	ActivityCompleted = "completed"
	ActivityFailed    = "failed"
	ActivityAbandoned = "abandoned"
)

// Activity type constants for stateful worker dispatches.
const (
	OpStart     = "start"
	OpStop      = "stop"
	OpRestart   = "restart"
	OpUpdate    = "update"
	OpReinstall = "reinstall"
	OpMigrate   = "migrate"
	OpBackup    = "backup"
	OpRestore   = "restore"
	OpArchive   = "archive"
	OpUnarchive = "unarchive"
)

type Activity struct {
	ID           string          `json:"id"`
	GameserverID *string         `json:"gameserver_id,omitempty"`
	WorkerID     string          `json:"worker_id,omitempty"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	Actor        json.RawMessage `json:"actor"`
	Data         json.RawMessage `json:"data"`
	Error        string          `json:"error,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
}

type ActivityFilter struct {
	GameserverID *string
	Type         *string
	Status       *string
	WorkerID     *string
	Pagination
}
