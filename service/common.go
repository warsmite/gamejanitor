package service

// Gameserver status constants.
// Lifecycle: Stopped → Pulling → Starting → Started → Running → Stopping → Stopped
// Gameserver statuses — reflect current container state, not the operation that triggered it.
const (
	StatusStopped    = "stopped"
	StatusInstalling = "installing"
	StatusStarting   = "starting"
	StatusStarted    = "started"
	StatusRunning    = "running"
	StatusStopping   = "stopping"
	StatusError      = "error"
)

// Container contract constants — shared between gamejanitor and game container scripts.
// Changing these requires updating the corresponding entrypoint.sh in images/base/.
const (
	InstallMarker = "[gamejanitor:installed]" // Emitted by entrypoint.sh after first install completes
	EnvSkipInstall = "SKIP_INSTALL=1"         // Passed to container when gs.Installed is true

	PortNameQuery = "query" // Port used for server query polling (A2S/GJQ)
	PortNameGame  = "game"  // Fallback port for query polling if no "query" port defined
)

// Disabled capability names — used in game definitions to opt out of features.
const CapabilityQuery = "query"

func needsRecovery(status string) bool {
	return status != StatusStopped && status != StatusError
}

func isRunningStatus(status string) bool {
	return status == StatusStarted || status == StatusRunning
}

// isPollableStatus returns true if a gameserver is in a state where polling should continue.
// This is more permissive than isRunningStatus — it includes transitional states like
// "installing" and "starting" because pollers are started early and must survive until
// the gameserver reaches a terminal state.
func isPollableStatus(status string) bool {
	return status != StatusStopped && status != StatusStopping && status != StatusError
}

