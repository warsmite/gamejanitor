package model

import (
	"encoding/json"
	"time"
)

// Operation type constants — used as event types for lifecycle operations
// and as the value of Gameserver.OperationType while an operation is in progress.
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
	OpDelete    = "delete"
)

// Event represents a persisted event in the events table.
type Event struct {
	ID           string          `json:"id"`
	GameserverID *string         `json:"gameserver_id,omitempty"`
	WorkerID     string          `json:"worker_id,omitempty"`
	Type         string          `json:"type"`
	Actor        json.RawMessage `json:"actor"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at"`
}

type EventFilter struct {
	GameserverID *string
	Type         *string
	WorkerID     *string
	Pagination
}
