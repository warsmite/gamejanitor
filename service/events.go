package service

import (
	"context"
	"time"

	"github.com/warsmite/gamejanitor/models"
)

// Action events — user/schedule initiated, carry actor
const (
	EventGameserverCreate  = "gameserver.create"
	EventGameserverUpdate  = "gameserver.update"
	EventGameserverDelete  = "gameserver.delete"
	EventGameserverStart      = "gameserver.start"
	EventGameserverStop       = "gameserver.stop"
	EventGameserverRestart    = "gameserver.restart"
	EventGameserverUpdateGame = "gameserver.update_game"
	EventGameserverReinstall  = "gameserver.reinstall"
	EventGameserverMigrate    = "gameserver.migrate"
	EventBackupCreate      = "backup.create"
	EventBackupDelete      = "backup.delete"
	EventBackupRestore     = "backup.restore"
	EventScheduleCreate    = "schedule.create"
	EventScheduleUpdate    = "schedule.update"
	EventScheduleDelete    = "schedule.delete"
	EventModInstalled      = "mod.installed"
	EventModUninstalled    = "mod.uninstalled"
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
	EventWorkerUpdated          = "worker.updated"
	EventScheduleTaskCompleted  = "schedule.task.completed"
	EventScheduleTaskFailed     = "schedule.task.failed"
	EventGameserverStats        = "gameserver.stats"
	EventGameserverQuery        = "gameserver.query"
)

// AllEventTypes is every event type, used for webhook endpoint validation.
var AllEventTypes = []string{
	// Action events
	EventGameserverCreate, EventGameserverUpdate, EventGameserverDelete,
	EventGameserverStart, EventGameserverStop, EventGameserverRestart,
	EventGameserverUpdateGame, EventGameserverReinstall, EventGameserverMigrate,
	EventBackupCreate, EventBackupDelete, EventBackupRestore,
	EventScheduleCreate, EventScheduleUpdate, EventScheduleDelete,
	EventModInstalled, EventModUninstalled,
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
	EventWorkerConnected, EventWorkerDisconnected, EventWorkerUpdated,
	EventScheduleTaskCompleted, EventScheduleTaskFailed,
	EventGameserverStats, EventGameserverQuery,
}

// Lifecycle events — published by lifecycle code, consumed by StatusSubscriber to derive status.

type ImagePullingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ImagePullingEvent) EventType() string        { return EventImagePulling }
func (e ImagePullingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ImagePullingEvent) EventGameserverID() string { return e.GameserverID }

type ImagePulledEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ImagePulledEvent) EventType() string        { return EventImagePulled }
func (e ImagePulledEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ImagePulledEvent) EventGameserverID() string { return e.GameserverID }

type ContainerCreatingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerCreatingEvent) EventType() string        { return EventContainerCreating }
func (e ContainerCreatingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerCreatingEvent) EventGameserverID() string { return e.GameserverID }

type ContainerStartedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStartedEvent) EventType() string        { return EventContainerStarted }
func (e ContainerStartedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStartedEvent) EventGameserverID() string { return e.GameserverID }

type GameserverReadyEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverReadyEvent) EventType() string        { return EventGameserverReady }
func (e GameserverReadyEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverReadyEvent) EventGameserverID() string { return e.GameserverID }

type ContainerStoppingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStoppingEvent) EventType() string        { return EventContainerStopping }
func (e ContainerStoppingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStoppingEvent) EventGameserverID() string { return e.GameserverID }

type ContainerStoppedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStoppedEvent) EventType() string        { return EventContainerStopped }
func (e ContainerStoppedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStoppedEvent) EventGameserverID() string { return e.GameserverID }

type ContainerExitedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerExitedEvent) EventType() string        { return EventContainerExited }
func (e ContainerExitedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerExitedEvent) EventGameserverID() string { return e.GameserverID }

type GameserverErrorEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Reason       string    `json:"reason"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverErrorEvent) EventType() string        { return EventGameserverError }
func (e GameserverErrorEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverErrorEvent) EventGameserverID() string { return e.GameserverID }

type GameserverStatsEvent struct {
	GameserverID    string    `json:"gameserver_id"`
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryUsageMB   int       `json:"memory_usage_mb"`
	MemoryLimitMB   int       `json:"memory_limit_mb"`
	VolumeSizeBytes int64     `json:"volume_size_bytes"`
	StorageLimitMB  *int      `json:"storage_limit_mb"`
	Timestamp       time.Time `json:"timestamp"`
}

func (e GameserverStatsEvent) EventType() string        { return EventGameserverStats }
func (e GameserverStatsEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverStatsEvent) EventGameserverID() string { return e.GameserverID }

type GameserverQueryEvent struct {
	GameserverID  string   `json:"gameserver_id"`
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
	Timestamp     time.Time `json:"timestamp"`
}

func (e GameserverQueryEvent) EventType() string        { return EventGameserverQuery }
func (e GameserverQueryEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverQueryEvent) EventGameserverID() string { return e.GameserverID }

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

// Action events — include full resource state so webhook consumers never need a follow-up API call.

type GameserverActionEvent struct {
	Type         string             `json:"type"`
	Timestamp    time.Time          `json:"timestamp"`
	Actor        Actor              `json:"actor"`
	GameserverID string             `json:"gameserver_id"`
	Gameserver   *models.Gameserver `json:"gameserver"`
}

func (e GameserverActionEvent) EventType() string        { return e.Type }
func (e GameserverActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverActionEvent) EventGameserverID() string { return e.GameserverID }

type BackupActionEvent struct {
	Type         string         `json:"type"`
	Timestamp    time.Time      `json:"timestamp"`
	Actor        Actor          `json:"actor"`
	GameserverID string         `json:"gameserver_id"`
	Backup       *models.Backup `json:"backup"`
	Error        string         `json:"error,omitempty"`
}

func (e BackupActionEvent) EventType() string        { return e.Type }
func (e BackupActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e BackupActionEvent) EventGameserverID() string { return e.GameserverID }

type WorkerActionEvent struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Actor     Actor       `json:"actor"`
	WorkerID  string      `json:"worker_id"`
	Worker    *WorkerView `json:"worker,omitempty"`
}

func (e WorkerActionEvent) EventType() string        { return e.Type }
func (e WorkerActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e WorkerActionEvent) EventGameserverID() string { return "" }

type ScheduleActionEvent struct {
	Type         string           `json:"type"`
	Timestamp    time.Time        `json:"timestamp"`
	Actor        Actor            `json:"actor"`
	GameserverID string           `json:"gameserver_id"`
	Schedule     *models.Schedule `json:"schedule"`
}

func (e ScheduleActionEvent) EventType() string        { return e.Type }
func (e ScheduleActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ScheduleActionEvent) EventGameserverID() string { return e.GameserverID }

type ScheduledTaskEvent struct {
	Type         string           `json:"type"`
	Timestamp    time.Time        `json:"timestamp"`
	Actor        Actor            `json:"actor"`
	GameserverID string           `json:"gameserver_id"`
	Schedule     *models.Schedule `json:"schedule"`
	TaskType     string           `json:"task_type"`
	Error        string           `json:"error,omitempty"`
}

func (e ScheduledTaskEvent) EventType() string        { return e.Type }
func (e ScheduledTaskEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ScheduledTaskEvent) EventGameserverID() string { return e.GameserverID }

type ModActionEvent struct {
	Type         string              `json:"type"`
	Timestamp    time.Time           `json:"timestamp"`
	Actor        Actor               `json:"actor"`
	GameserverID string              `json:"gameserver_id"`
	Mod          *models.InstalledMod `json:"mod"`
}

func (e ModActionEvent) EventType() string        { return e.Type }
func (e ModActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ModActionEvent) EventGameserverID() string { return e.GameserverID }
