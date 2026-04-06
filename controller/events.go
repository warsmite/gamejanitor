package controller

import (
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
	EventGameserverArchive   = "gameserver.archive"
	EventGameserverUnarchive = "gameserver.unarchive"
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
	EventDepotDownloading  = "gameserver.depot_downloading"
	EventDepotComplete     = "gameserver.depot_complete"
	EventDepotCached       = "gameserver.depot_cached"
	EventImagePulling      = "gameserver.image_pulling"
	EventImagePulled       = "gameserver.image_pulled"
	EventInstanceCreating = "gameserver.instance_creating"
	EventInstanceStarted  = "gameserver.instance_started"
	EventGameserverReady   = "gameserver.ready"
	EventInstanceStopping = "gameserver.instance_stopping"
	EventInstanceStopped  = "gameserver.instance_stopped"
	EventInstanceExited   = "gameserver.instance_exited"
	EventGameserverError   = "gameserver.error"
	EventGameserverOperation = "gameserver.operation"
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
	EventGameserverStatusChanged = "gameserver.status_changed"
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
	EventDepotDownloading, EventDepotComplete, EventDepotCached,
	EventImagePulling, EventImagePulled,
	EventInstanceCreating, EventInstanceStarted,
	EventGameserverReady,
	EventInstanceStopping, EventInstanceStopped, EventInstanceExited,
	EventGameserverError,
	EventGameserverOperation,
	// Operation outcomes

	EventBackupCompleted, EventBackupFailed,
	EventBackupRestoreCompleted, EventBackupRestoreFailed,
	EventWorkerConnected, EventWorkerDisconnected, EventWorkerUpdated,
	EventScheduleTaskCompleted, EventScheduleTaskFailed, EventScheduleTaskMissed,
	EventGameserverStatusChanged,
	EventGameserverStats, EventGameserverQuery,
	// Warnings & checks
	EventGameserverWarning,
	EventGameserverReachable,
}

// Lifecycle events — published by lifecycle code, consumed by StatusSubscriber to derive status.

type DepotDownloadingEvent struct {
	GameserverID string    `json:"gameserver_id"`
	AppID        uint32    `json:"app_id"`
	TotalBytes   uint64    `json:"total_bytes"`
	TotalChunks  int       `json:"total_chunks"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e DepotDownloadingEvent) EventType() string        { return EventDepotDownloading }
func (e DepotDownloadingEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e DepotDownloadingEvent) EventGameserverID() string { return e.GameserverID }
func (e DepotDownloadingEvent) EventActor() Actor         { return SystemActor }

type DepotCompleteEvent struct {
	GameserverID    string    `json:"gameserver_id"`
	AppID           uint32    `json:"app_id"`
	BytesDownloaded uint64    `json:"bytes_downloaded"`
	IsDelta         bool      `json:"is_delta"`
	Timestamp       time.Time `json:"timestamp"`
}

func (e DepotCompleteEvent) EventType() string        { return EventDepotComplete }
func (e DepotCompleteEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e DepotCompleteEvent) EventGameserverID() string { return e.GameserverID }
func (e DepotCompleteEvent) EventActor() Actor         { return SystemActor }

type DepotCachedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	AppID        uint32    `json:"app_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e DepotCachedEvent) EventType() string        { return EventDepotCached }
func (e DepotCachedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e DepotCachedEvent) EventGameserverID() string { return e.GameserverID }
func (e DepotCachedEvent) EventActor() Actor         { return SystemActor }

type OperationEvent struct {
	GameserverID string           `json:"gameserver_id"`
	Operation    *model.Operation `json:"operation"` // nil when operation cleared
	Timestamp    time.Time        `json:"timestamp"`
}

func (e OperationEvent) EventType() string        { return EventGameserverOperation }
func (e OperationEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e OperationEvent) EventGameserverID() string { return e.GameserverID }
func (e OperationEvent) EventActor() Actor         { return SystemActor }

// LifecycleEvent is a system-initiated event that carries only a gameserver ID
// and timestamp. Covers: image pulling/pulled, instance creating/started/stopping/stopped/exited,
// and gameserver ready.
type LifecycleEvent struct {
	Type_        string    `json:"type"`
	GameserverID string    `json:"gameserver_id"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e LifecycleEvent) EventType() string        { return e.Type_ }
func (e LifecycleEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e LifecycleEvent) EventGameserverID() string { return e.GameserverID }
func (e LifecycleEvent) EventActor() Actor         { return SystemActor }

type GameserverErrorEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Reason       string    `json:"reason"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverErrorEvent) EventType() string        { return EventGameserverError }
func (e GameserverErrorEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverErrorEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverErrorEvent) EventActor() Actor          { return SystemActor }

// GameserverStatusChangedEvent carries the derived display status so SSE/webhook
// consumers don't need to replicate DeriveStatus logic. Published by the
// StatusManager whenever a worker state update changes the gameserver's status.
type GameserverStatusChangedEvent struct {
	GameserverID string    `json:"gameserver_id"`
	Status       string    `json:"status"`
	ErrorReason  string    `json:"error_reason,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
}

func (e GameserverStatusChangedEvent) EventType() string        { return EventGameserverStatusChanged }
func (e GameserverStatusChangedEvent) EventTimestamp() time.Time { return e.Timestamp }
func (e GameserverStatusChangedEvent) EventGameserverID() string { return e.GameserverID }
func (e GameserverStatusChangedEvent) EventActor() Actor         { return SystemActor }

type GameserverStatsEvent struct {
	GameserverID    string    `json:"gameserver_id"`
	CPUPercent      float64   `json:"cpu_percent"`
	MemoryUsageMB   int       `json:"memory_usage_mb"`
	MemoryLimitMB   int       `json:"memory_limit_mb"`
	NetRxBytes      int64     `json:"net_rx_bytes"`
	NetTxBytes      int64     `json:"net_tx_bytes"`
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

// EventTypeForOp maps an operation type (model.OpStart, etc.) to its event type constant.
func EventTypeForOp(op string) string {
	switch op {
	case model.OpStart:
		return EventGameserverStart
	case model.OpStop:
		return EventGameserverStop
	case model.OpRestart:
		return EventGameserverRestart
	case model.OpUpdate:
		return EventGameserverUpdateGame
	case model.OpReinstall:
		return EventGameserverReinstall
	case model.OpMigrate:
		return EventGameserverMigrate
	case model.OpBackup:
		return EventBackupCreate
	case model.OpRestore:
		return EventBackupRestore
	case model.OpArchive:
		return EventGameserverArchive
	case model.OpUnarchive:
		return EventGameserverUnarchive
	default:
		return "gameserver." + op
	}
}
