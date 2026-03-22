package service

import (
	"context"
	"time"
)

// actorTokenID extracts the token ID from context, or nil for system/async actions.
func actorTokenID(ctx context.Context) *string {
	token := TokenFromContext(ctx)
	if token == nil {
		return nil
	}
	return &token.ID
}

type GameserverEvent struct {
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	ActorTokenID  *string   `json:"actor_token_id,omitempty"`
	GameserverID  string    `json:"gameserver_id"`
	Name          string    `json:"name"`
	GameID        string    `json:"game_id"`
	NodeID        *string   `json:"node_id"`
	MemoryLimitMB int       `json:"memory_limit_mb"`
}

func (e GameserverEvent) EventType() string        { return e.Type }
func (e GameserverEvent) EventTimestamp() time.Time { return e.Timestamp }

type BackupEvent struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	ActorTokenID *string   `json:"actor_token_id,omitempty"`
	GameserverID string    `json:"gameserver_id"`
	BackupID     string    `json:"backup_id"`
	BackupName   string    `json:"backup_name,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func (e BackupEvent) EventType() string        { return e.Type }
func (e BackupEvent) EventTimestamp() time.Time { return e.Timestamp }

type WorkerEvent struct {
	Type      string    `json:"type"`
	Timestamp time.Time `json:"timestamp"`
	WorkerID  string    `json:"worker_id"`
}

func (e WorkerEvent) EventType() string        { return e.Type }
func (e WorkerEvent) EventTimestamp() time.Time { return e.Timestamp }

type ScheduledTaskEvent struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	GameserverID string    `json:"gameserver_id"`
	ScheduleID   string    `json:"schedule_id"`
	TaskType     string    `json:"task_type"`
	Error        string    `json:"error,omitempty"`
}

func (e ScheduledTaskEvent) EventType() string        { return e.Type }
func (e ScheduledTaskEvent) EventTimestamp() time.Time { return e.Timestamp }
