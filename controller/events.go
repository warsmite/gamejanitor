package controller

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"context"
	"time"

	"github.com/warsmite/gamejanitor/model"
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
	EventBackupCompleted        = "backup.completed"
	EventBackupFailed           = "backup.failed"
	EventBackupRestoreCompleted = "backup.restore.completed"
	EventBackupRestoreFailed    = "backup.restore.failed"
	EventWorkerConnected        = "worker.connected"
	EventWorkerDisconnected     = "worker.disconnected"
	EventWorkerUpdated          = "worker.updated"
	EventScheduleTaskCompleted  = "schedule.task.completed"
	EventScheduleTaskFailed     = "schedule.task.failed"
	EventScheduleTaskMissed     = "schedule.task.missed"
	EventGameserverStats        = "gameserver.stats"
	EventGameserverQuery        = "gameserver.query"
	EventGameserverWarning      = "gameserver.warning"
	EventGameserverReachable    = "gameserver.reachable"
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
	EventScheduleTaskCompleted, EventScheduleTaskFailed, EventScheduleTaskMissed,
	EventGameserverStats, EventGameserverQuery,
	// Warnings & checks
	EventGameserverWarning,
	EventGameserverReachable,
}

// Lifecycle events — published by lifecycle code, consumed by StatusSubscriber to derive status.

type ImagePullingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ImagePullingEvent) EventType() string        { return EventImagePulling }
func (e ImagePullingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ImagePullingEvent) EventGameserverID() string { return e.GameserverID }
func (e ImagePullingEvent) EventActor() Actor          { return SystemActor }

type ImagePulledEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ImagePulledEvent) EventType() string        { return EventImagePulled }
func (e ImagePulledEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ImagePulledEvent) EventGameserverID() string { return e.GameserverID }
func (e ImagePulledEvent) EventActor() Actor          { return SystemActor }

type ContainerCreatingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerCreatingEvent) EventType() string        { return EventContainerCreating }
func (e ContainerCreatingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerCreatingEvent) EventGameserverID() string { return e.GameserverID }
func (e ContainerCreatingEvent) EventActor() Actor          { return SystemActor }

type ContainerStartedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStartedEvent) EventType() string        { return EventContainerStarted }
func (e ContainerStartedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStartedEvent) EventGameserverID() string { return e.GameserverID }
func (e ContainerStartedEvent) EventActor() Actor          { return SystemActor }

type GameserverReadyEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverReadyEvent) EventType() string        { return EventGameserverReady }
func (e GameserverReadyEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverReadyEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverReadyEvent) EventActor() Actor          { return SystemActor }

type ContainerStoppingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStoppingEvent) EventType() string        { return EventContainerStopping }
func (e ContainerStoppingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStoppingEvent) EventGameserverID() string { return e.GameserverID }
func (e ContainerStoppingEvent) EventActor() Actor          { return SystemActor }

type ContainerStoppedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerStoppedEvent) EventType() string        { return EventContainerStopped }
func (e ContainerStoppedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerStoppedEvent) EventGameserverID() string { return e.GameserverID }
func (e ContainerStoppedEvent) EventActor() Actor          { return SystemActor }

type ContainerExitedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e ContainerExitedEvent) EventType() string        { return EventContainerExited }
func (e ContainerExitedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ContainerExitedEvent) EventGameserverID() string { return e.GameserverID }
func (e ContainerExitedEvent) EventActor() Actor          { return SystemActor }

type GameserverErrorEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Reason       string    `json:"reason"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverErrorEvent) EventType() string        { return EventGameserverError }
func (e GameserverErrorEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverErrorEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverErrorEvent) EventActor() Actor          { return SystemActor }

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
func (e GameserverStatsEvent) EventActor() Actor          { return SystemActor }

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
func (e GameserverQueryEvent) EventActor() Actor          { return SystemActor }

// GameserverWarningEvent is a unified warning event. All warnings use the same
// event type with category/level distinguishing them. This avoids event type
// proliferation — businesses subscribe to "gameserver.warning" once and get
// all current and future warning categories.
type GameserverWarningEvent struct {
	GameserverID string         `json:"gameserver_id"`
	Category     string         `json:"category"`       // "storage", "memory", "cpu", etc.
	Level        string         `json:"level"`           // "warning", "critical", "resolved"
	Message      string         `json:"message"`
	Data         map[string]any `json:"data,omitempty"`  // category-specific details
	Timestamp    time.Time      `json:"timestamp"`
}

func (e GameserverWarningEvent) EventType() string        { return EventGameserverWarning }
func (e GameserverWarningEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverWarningEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverWarningEvent) EventActor() Actor         { return SystemActor }

// GameserverReachableEvent is emitted after probing a gameserver's public
// reachability via the gamejanitor browser service.
type GameserverReachableEvent struct {
	GameserverID string `json:"gameserver_id"`
	Reachable    bool   `json:"reachable"`
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Registered   bool   `json:"registered,omitempty"` // added to server browser
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverReachableEvent) EventType() string        { return EventGameserverReachable }
func (e GameserverReachableEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverReachableEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverReachableEvent) EventActor() Actor         { return SystemActor }

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
	if token := auth.TokenFromContext(ctx); token != nil {
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
	Gameserver   *model.Gameserver `json:"gameserver"`
}

func (e GameserverActionEvent) EventType() string        { return e.Type }
func (e GameserverActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverActionEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverActionEvent) EventActor() Actor          { return e.Actor }

type BackupActionEvent struct {
	Type         string         `json:"type"`
	Timestamp    time.Time      `json:"timestamp"`
	Actor        Actor          `json:"actor"`
	GameserverID string         `json:"gameserver_id"`
	Backup       *model.Backup `json:"backup"`
	Error        string         `json:"error,omitempty"`
}

func (e BackupActionEvent) EventType() string        { return e.Type }
func (e BackupActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e BackupActionEvent) EventGameserverID() string { return e.GameserverID }
func (e BackupActionEvent) EventActor() Actor          { return e.Actor }

type WorkerActionEvent struct {
	Type      string      `json:"type"`
	Timestamp time.Time   `json:"timestamp"`
	Actor     Actor       `json:"actor"`
	WorkerID  string      `json:"worker_id"`
	Worker    any `json:"worker,omitempty"`
}

func (e WorkerActionEvent) EventType() string        { return e.Type }
func (e WorkerActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e WorkerActionEvent) EventGameserverID() string { return "" }
func (e WorkerActionEvent) EventActor() Actor          { return e.Actor }

type ScheduleActionEvent struct {
	Type         string           `json:"type"`
	Timestamp    time.Time        `json:"timestamp"`
	Actor        Actor            `json:"actor"`
	GameserverID string           `json:"gameserver_id"`
	Schedule     *model.Schedule `json:"schedule"`
}

func (e ScheduleActionEvent) EventType() string        { return e.Type }
func (e ScheduleActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ScheduleActionEvent) EventGameserverID() string { return e.GameserverID }
func (e ScheduleActionEvent) EventActor() Actor          { return e.Actor }

type ScheduledTaskEvent struct {
	Type         string           `json:"type"`
	Timestamp    time.Time        `json:"timestamp"`
	Actor        Actor            `json:"actor"`
	GameserverID string           `json:"gameserver_id"`
	Schedule     *model.Schedule `json:"schedule"`
	TaskType     string           `json:"task_type"`
	Error        string           `json:"error,omitempty"`
}

func (e ScheduledTaskEvent) EventType() string        { return e.Type }
func (e ScheduledTaskEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ScheduledTaskEvent) EventGameserverID() string { return e.GameserverID }
func (e ScheduledTaskEvent) EventActor() Actor          { return e.Actor }

type ModActionEvent struct {
	Type         string              `json:"type"`
	Timestamp    time.Time           `json:"timestamp"`
	Actor        Actor               `json:"actor"`
	GameserverID string              `json:"gameserver_id"`
	Mod          *model.InstalledMod `json:"mod"`
}

func (e ModActionEvent) EventType() string        { return e.Type }
func (e ModActionEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e ModActionEvent) EventGameserverID() string { return e.GameserverID }
func (e ModActionEvent) EventActor() Actor          { return e.Actor }
