package model

// OperationPhase describes the current step within a gameserver operation.
type OperationPhase string

const (
	PhaseDownloadingGame OperationPhase = "downloading_game"
	PhasePullingImage    OperationPhase = "pulling_image"
	PhaseInstalling      OperationPhase = "installing"
	PhaseStarting        OperationPhase = "starting"
	PhaseStopping        OperationPhase = "stopping"
	PhaseCreatingBackup  OperationPhase = "creating_backup"
	PhaseRestoringBackup OperationPhase = "restoring_backup"
	PhaseUpdatingGame    OperationPhase = "updating_game"
	PhaseReinstalling    OperationPhase = "reinstalling"
	PhaseMigrating       OperationPhase = "migrating"
	PhaseDeleting        OperationPhase = "deleting"
)

// OperationProgress carries optional progress data within a phase.
type OperationProgress struct {
	Percent        float64 `json:"percent"`
	CompletedBytes uint64  `json:"completed_bytes,omitempty"`
	TotalBytes     uint64  `json:"total_bytes,omitempty"`
}

// Operation represents the current in-flight operation on a gameserver.
// Transient — held in memory, not persisted to DB.
type Operation struct {
	Type     string             `json:"type"`               // "start", "stop", "backup", "restore", "update_game", "reinstall", "migrate"
	Phase    OperationPhase     `json:"phase"`
	Progress *OperationProgress `json:"progress,omitempty"`
}
