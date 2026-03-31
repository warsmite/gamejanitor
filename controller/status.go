package controller

// Gameserver status constants.
// Lifecycle: Stopped → Pulling → Starting → Started → Running → Stopping → Stopped
// Gameserver statuses — reflect current instance state, not the operation that triggered it.
const (
	StatusStopped     = "stopped"
	StatusInstalling  = "installing"
	StatusStarting    = "starting"
	StatusStarted     = "started"
	StatusRunning     = "running"
	StatusStopping    = "stopping"
	StatusError       = "error"
	StatusUnreachable = "unreachable" // Worker disconnected — actual instance state unknown
	StatusArchived    = "archived"
)

// Instance contract constants — shared between gamejanitor and game instance scripts.
// Changing these requires updating the corresponding entrypoint.sh in images/base/.
const (
	InstallMarker = "[gamejanitor:installed]" // Emitted by entrypoint.sh after first install completes
	EnvSkipInstall = "SKIP_INSTALL=1"         // Passed to instance when gs.Installed is true

	PortNameQuery = "query" // Port used for server query polling (A2S/GJQ)
	PortNameGame  = "game"  // Fallback port for query polling if no "query" port defined
)

// Disabled capability names — used in game definitions to opt out of features.
const CapabilityQuery = "query"

func NeedsRecovery(status string) bool {
	return status != StatusStopped && status != StatusError
}

// NeedsRecoveryOnReconnect returns true for statuses that should be reconciled
// when a worker comes back online — includes unreachable gameservers.
func NeedsRecoveryOnReconnect(status string) bool {
	return NeedsRecovery(status) || status == StatusUnreachable
}

func IsRunningStatus(status string) bool {
	return status == StatusStarted || status == StatusRunning
}

// IsPollableStatus returns true if a gameserver is in a state where polling should continue.
// This is more permissive than IsRunningStatus — it includes transitional states like
// "installing" and "starting" because pollers are started early and must survive until
// the gameserver reaches a terminal state.
func IsPollableStatus(status string) bool {
	return status != StatusStopped && status != StatusStopping && status != StatusError
}
