package service

import (
	"context"
	"time"
)

// Action events — user/schedule initiated, carry actor
const (
	EventGameserverCreate  = "gameserver.create"
	EventGameserverUpdate  = "gameserver.update"
	EventGameserverDelete  = "gameserver.delete"
	EventGameserverStart   = "gameserver.start"
	EventGameserverStop    = "gameserver.stop"
	EventGameserverRestart = "gameserver.restart"
	EventBackupCreate      = "backup.create"
	EventBackupDelete      = "backup.delete"
	EventBackupRestore     = "backup.restore"
	EventScheduleCreate    = "schedule.create"
	EventScheduleUpdate    = "schedule.update"
	EventScheduleDelete    = "schedule.delete"
)

// Lifecycle outcome events — system, drive status changes
const (
	EventImagePulling      = "gameserver.image_pulling"
	EventImagePulled       = "gameserver.image_pulled"
	EventContainerCreating = "gameserver.container_creating"
	EventContainerStarted  = "gameserver.container_started"
	EventGameserverReady   = "gameserver.ready"
	EventContainerStopping = "gameserver.container_stopping"
	EventContainerStopped  = "gameserver.container_stopped"
	EventContainerExited   = "gameserver.container_exited"
	EventGameserverError   = "gameserver.error"
)

// Operation outcome events — system
const (
	EventStatusChanged          = "status_changed"
	EventBackupCompleted        = "backup.completed"
	EventBackupFailed           = "backup.failed"
	EventBackupRestoreCompleted = "backup.restore.completed"
	EventBackupRestoreFailed    = "backup.restore.failed"
	EventWorkerConnected        = "worker.connected"
	EventWorkerDisconnected     = "worker.disconnected"
	EventScheduleTaskCompleted  = "schedule.task.completed"
	EventScheduleTaskFailed     = "schedule.task.failed"
)

// AllEventTypes is every event type, used for webhook endpoint validation.
var AllEventTypes = []string{
	// Action events
	EventGameserverCreate, EventGameserverUpdate, EventGameserverDelete,
	EventGameserverStart, EventGameserverStop, EventGameserverRestart,
	EventBackupCreate, EventBackupDelete, EventBackupRestore,
	EventScheduleCreate, EventScheduleUpdate, EventScheduleDelete,
	// Lifecycle outcomes
	EventImagePulling, EventImagePulled,
	EventContainerCreating, EventContainerStarted,
	EventGameserverReady,
	EventContainerStopping, EventContainerStopped, EventContainerExited,
	EventGameserverError,
	// Operation outcomes
	EventStatusChanged,
	EventBackupCompleted, EventBackupFailed,
	EventBackupRestoreCompleted, EventBackupRestoreFailed,
	EventWorkerConnected, EventWorkerDisconnected,
	EventScheduleTaskCompleted, EventScheduleTaskFailed,
}

// Lifecycle events — published by lifecycle code, consumed by StatusSubscriber to derive status.

type ImagePullingEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ImagePullingEvent) EventType() string        { return EventImagePulling }
func (e ImagePullingEvent) EventTimestamp() time.Time { return e.Timestamp }

type ImagePulledEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ImagePulledEvent) EventType() string        { return EventImagePulled }
func (e ImagePulledEvent) EventTimestamp() time.Time { return e.Timestamp }

type ContainerCreatingEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ContainerCreatingEvent) EventType() string        { return EventContainerCreating }
func (e ContainerCreatingEvent) EventTimestamp() time.Time { return e.Timestamp }

type ContainerStartedEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ContainerStartedEvent) EventType() string        { return EventContainerStarted }
func (e ContainerStartedEvent) EventTimestamp() time.Time { return e.Timestamp }

type GameserverReadyEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e GameserverReadyEvent) EventType() string        { return EventGameserverReady }
func (e GameserverReadyEvent) EventTimestamp() time.Time { return e.Timestamp }

type ContainerStoppingEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ContainerStoppingEvent) EventType() string        { return EventContainerStopping }
func (e ContainerStoppingEvent) EventTimestamp() time.Time { return e.Timestamp }

type ContainerStoppedEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ContainerStoppedEvent) EventType() string        { return EventContainerStopped }
func (e ContainerStoppedEvent) EventTimestamp() time.Time { return e.Timestamp }

type ContainerExitedEvent struct {
	GameserverID string
	Timestamp    time.Time
}

func (e ContainerExitedEvent) EventType() string        { return EventContainerExited }
func (e ContainerExitedEvent) EventTimestamp() time.Time { return e.Timestamp }

type GameserverErrorEvent struct {
	GameserverID string
	Reason       string
	Timestamp    time.Time
}

func (e GameserverErrorEvent) EventType() string        { return EventGameserverError }
func (e GameserverErrorEvent) EventTimestamp() time.Time { return e.Timestamp }

// Actor represents who/what initiated an action.
type Actor struct {
	Type       string `json:"type"`                  // "token", "schedule", "system", "anonymous"
	TokenID    string `json:"token_id,omitempty"`
	ScheduleID string `json:"schedule_id,omitempty"`
}

var SystemActor = Actor{Type: "system"}

type actorContextKey struct{}

// SetActorInContext stores an actor in the context for downstream event publishing.
func SetActorInContext(ctx context.Context, actor Actor) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFromContext extracts the actor from context.
// Returns anonymous actor if no actor is set (auth disabled).
func ActorFromContext(ctx context.Context) Actor {
	if a, ok := ctx.Value(actorContextKey{}).(Actor); ok {
		return a
	}
	// Fall back to token-based actor for backward compatibility with auth middleware
	if token := TokenFromContext(ctx); token != nil {
		return Actor{Type: "token", TokenID: token.ID}
	}
	return Actor{Type: "anonymous"}
}

type GameserverEvent struct {
	Type          string    `json:"type"`
	Timestamp     time.Time `json:"timestamp"`
	Actor         Actor     `json:"actor"`
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
	Actor        Actor     `json:"actor"`
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
	Actor     Actor     `json:"actor"`
	WorkerID  string    `json:"worker_id"`
}

func (e WorkerEvent) EventType() string        { return e.Type }
func (e WorkerEvent) EventTimestamp() time.Time { return e.Timestamp }

type ScheduledTaskEvent struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	Actor        Actor     `json:"actor"`
	GameserverID string    `json:"gameserver_id"`
	ScheduleID   string    `json:"schedule_id"`
	TaskType     string    `json:"task_type"`
	Error        string    `json:"error,omitempty"`
}

func (e ScheduledTaskEvent) EventType() string        { return e.Type }
func (e ScheduledTaskEvent) EventTimestamp() time.Time { return e.Timestamp }
