package event

import (
	"encoding/json"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

// Action events — user/schedule initiated, carry actor
const (
	EventGameserverCreate     = "gameserver.create"
	EventGameserverUpdate     = "gameserver.update"
	EventGameserverDelete     = "gameserver.delete"
	EventGameserverStart      = "gameserver.start"
	EventGameserverStop       = "gameserver.stop"
	EventGameserverRestart    = "gameserver.restart"
	EventGameserverUpdateGame = "gameserver.update_game"
	EventGameserverReinstall  = "gameserver.reinstall"
	EventGameserverMigrate    = "gameserver.migrate"
	EventGameserverArchive    = "gameserver.archive"
	EventGameserverUnarchive  = "gameserver.unarchive"
	EventBackupCreate         = "backup.create"
	EventBackupDelete         = "backup.delete"
	EventBackupRestore        = "backup.restore"
	EventScheduleCreate       = "schedule.create"
	EventScheduleUpdate       = "schedule.update"
	EventScheduleDelete       = "schedule.delete"
	EventModInstalled         = "mod.installed"
	EventModUninstalled       = "mod.uninstalled"
)

// Lifecycle outcome events — system, drive status changes
const (
	EventDepotDownloading    = "gameserver.depot_downloading"
	EventDepotComplete       = "gameserver.depot_complete"
	EventDepotCached         = "gameserver.depot_cached"
	EventImagePulling        = "gameserver.image_pulling"
	EventImagePulled         = "gameserver.image_pulled"
	EventInstanceCreating    = "gameserver.instance_creating"
	EventInstanceStarted     = "gameserver.instance_started"
	EventGameserverReady     = "gameserver.ready"
	EventInstanceStopping    = "gameserver.instance_stopping"
	EventInstanceStopped     = "gameserver.instance_stopped"
	EventInstanceExited      = "gameserver.instance_exited"
	EventGameserverError     = "gameserver.error"
	EventGameserverOperation = "gameserver.operation"
)

// Operation outcome events — system
const (
	EventBackupCompleted         = "backup.completed"
	EventBackupFailed            = "backup.failed"
	EventBackupRestoreCompleted  = "backup.restore.completed"
	EventBackupRestoreFailed     = "backup.restore.failed"
	EventWorkerConnected         = "worker.connected"
	EventWorkerDisconnected      = "worker.disconnected"
	EventWorkerUpdated           = "worker.updated"
	EventScheduleTaskCompleted   = "schedule.task.completed"
	EventScheduleTaskFailed      = "schedule.task.failed"
	EventScheduleTaskMissed      = "schedule.task.missed"
	EventGameserverStats         = "gameserver.stats"
	EventGameserverQuery         = "gameserver.query"
	EventGameserverWarning       = "gameserver.warning"
	EventGameserverReachable     = "gameserver.reachable"
)

// AllEventTypes is every event type, used for webhook endpoint validation.
var AllEventTypes = []string{
	EventGameserverCreate, EventGameserverUpdate, EventGameserverDelete,
	EventGameserverStart, EventGameserverStop, EventGameserverRestart,
	EventGameserverUpdateGame, EventGameserverReinstall, EventGameserverMigrate,
	EventBackupCreate, EventBackupDelete, EventBackupRestore,
	EventScheduleCreate, EventScheduleUpdate, EventScheduleDelete,
	EventModInstalled, EventModUninstalled,
	EventDepotDownloading, EventDepotComplete, EventDepotCached,
	EventImagePulling, EventImagePulled,
	EventInstanceCreating, EventInstanceStarted,
	EventGameserverReady,
	EventInstanceStopping, EventInstanceStopped, EventInstanceExited,
	EventGameserverError,
	EventGameserverOperation,
	EventBackupCompleted, EventBackupFailed,
	EventBackupRestoreCompleted, EventBackupRestoreFailed,
	EventWorkerConnected, EventWorkerDisconnected, EventWorkerUpdated,
	EventScheduleTaskCompleted, EventScheduleTaskFailed, EventScheduleTaskMissed,
	EventGameserverStats, EventGameserverQuery,
	EventGameserverWarning,
	EventGameserverReachable,
}

// Event is the single event type published through the EventBus.
// The Data field carries event-specific payload — type-switch on it
// when you need access to the extra fields.
//
// MarshalJSON flattens the Data fields into the top-level JSON object so
// consumers see {"type":"...","gameserver_id":"...","cpu_percent":5.2,...}
// instead of nested {"type":"...","data":{"cpu_percent":5.2,...}}.
type Event struct {
	Type         string    `json:"type"`
	GameserverID string    `json:"gameserver_id,omitempty"`
	Actor        Actor     `json:"actor"`
	Timestamp    time.Time `json:"timestamp"`
	Data         any       `json:"-"` // excluded from default marshal, flattened by MarshalJSON
}

func (e Event) EventType() string        { return e.Type }
func (e Event) EventTimestamp() time.Time { return e.Timestamp }
func (e Event) EventGameserverID() string { return e.GameserverID }
func (e Event) EventActor() Actor         { return e.Actor }

func (e Event) MarshalJSON() ([]byte, error) {
	flat := map[string]any{
		"type":      e.Type,
		"actor":     e.Actor,
		"timestamp": e.Timestamp,
	}
	if e.GameserverID != "" {
		flat["gameserver_id"] = e.GameserverID
	}

	// Merge data fields into the flat map
	if e.Data != nil {
		dataBytes, err := json.Marshal(e.Data)
		if err != nil {
			return nil, err
		}
		var dataMap map[string]any
		if err := json.Unmarshal(dataBytes, &dataMap); err == nil {
			for k, v := range dataMap {
				flat[k] = v
			}
		}
	}

	return json.Marshal(flat)
}

// --- Event data types ---

type DepotDownloadingData struct {
	AppID      uint32 `json:"app_id"`
	TotalBytes uint64 `json:"total_bytes"`
	TotalChunks int   `json:"total_chunks"`
}

type DepotCompleteData struct {
	AppID           uint32 `json:"app_id"`
	BytesDownloaded uint64 `json:"bytes_downloaded"`
	IsDelta         bool   `json:"is_delta"`
}

type DepotCachedData struct {
	AppID uint32 `json:"app_id"`
}

type OperationData struct {
	Operation *model.Operation `json:"operation"` // nil when operation cleared
}

type ErrorData struct {
	Reason string `json:"reason"`
}

type StatsData struct {
	CPUPercent      float64 `json:"cpu_percent"`
	MemoryUsageMB   int     `json:"memory_usage_mb"`
	MemoryLimitMB   int     `json:"memory_limit_mb"`
	VolumeSizeBytes int64   `json:"volume_size_bytes"`
	StorageLimitMB  *int    `json:"storage_limit_mb"`
}

type QueryData struct {
	PlayersOnline int      `json:"players_online"`
	MaxPlayers    int      `json:"max_players"`
	Players       []string `json:"players"`
	Map           string   `json:"map"`
	Version       string   `json:"version"`
}

type WarningData struct {
	Category string         `json:"category"`
	Level    string         `json:"level"`
	Message  string         `json:"message"`
	Extra    map[string]any `json:"data,omitempty"`
}

type ReachableData struct {
	Reachable  bool   `json:"reachable"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Registered bool   `json:"registered,omitempty"`
}

type GameserverActionData struct {
	Gameserver *model.Gameserver `json:"gameserver"`
}

type BackupActionData struct {
	Backup *model.Backup `json:"backup"`
	Error  string        `json:"error,omitempty"`
}

type WorkerActionData struct {
	WorkerID string `json:"worker_id"`
	Worker   any    `json:"worker,omitempty"`
}

type ScheduleActionData struct {
	Schedule *model.Schedule `json:"schedule"`
}

type ScheduledTaskData struct {
	Schedule *model.Schedule `json:"schedule"`
	TaskType string          `json:"task_type"`
	Error    string          `json:"error,omitempty"`
}

type ModActionData struct {
	Mod *model.InstalledMod `json:"mod"`
}

// --- Constructors ---

func NewEvent(eventType, gameserverID string, actor Actor, data any) Event {
	return Event{
		Type:         eventType,
		GameserverID: gameserverID,
		Actor:        actor,
		Timestamp:    time.Now(),
		Data:         data,
	}
}

func NewSystemEvent(eventType, gameserverID string, data any) Event {
	return NewEvent(eventType, gameserverID, SystemActor, data)
}

// EventTypeForOp maps an operation type (model.OpStart, etc.) to its event type constant.
func EventTypeForOp(op model.OpType) string {
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
		return "gameserver." + string(op)
	}
}
