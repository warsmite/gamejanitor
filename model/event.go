package model

import (
	"encoding/json"
	"time"
)

// OpType names a kind of lifecycle operation. Used in Operation.Type, as the
// key for operation-priority rules in the controller, and as the value of the
// operation field on gameserver.operation events.
type OpType string

const (
	OpStart     OpType = "start"
	OpStop      OpType = "stop"
	OpRestart   OpType = "restart"
	OpUpdate    OpType = "update"
	OpReinstall OpType = "reinstall"
	OpMigrate   OpType = "migrate"
	OpBackup    OpType = "backup"
	OpRestore   OpType = "restore"
	OpArchive   OpType = "archive"
	OpUnarchive OpType = "unarchive"
	OpDelete    OpType = "delete"
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
