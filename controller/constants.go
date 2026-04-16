package controller

// ProcessState is the controller's view of a gameserver instance's lifecycle
// as reported by the worker. It is orthogonal to readiness: a process can be
// Running but not yet ready (the worker hasn't observed the ready pattern).
type ProcessState string

const (
	// ProcessNone means no worker instance exists for this gameserver — either
	// we never started one or the last one has been removed. This is the
	// resting state for a stopped gameserver.
	ProcessNone ProcessState = "none"

	// ProcessCreating is a transient state while the worker is preparing the
	// container but before it has been started.
	ProcessCreating ProcessState = "creating"

	// ProcessStarting means the process has been launched on the worker but
	// has not yet been observed as alive by the controller.
	ProcessStarting ProcessState = "starting"

	// ProcessRunning means the process is alive on the worker. Readiness
	// (whether it's accepting connections) is a separate signal — see
	// LiveGameserver.ready.
	ProcessRunning ProcessState = "running"

	// ProcessExited means the process has terminated. The exit code and
	// reason live alongside the state on the gameserver.
	ProcessExited ProcessState = "exited"
)

// Gameserver display statuses — derived from desired state + worker state + operation.
// DEPRECATED: these are on their way out alongside LiveGameserver.Status() and the
// gameserver.status_changed event. The controller is moving to expose primary facts
// (ProcessState, ready, errorReason, operation, worker connection) instead of a
// compressed enum. Consumers that want a single-word summary should derive one at
// their boundary.
const (
	StatusStopped     = "stopped"
	StatusInstalling  = "installing"
	StatusStarting    = "starting"
	StatusRunning     = "running"
	StatusStopping    = "stopping"
	StatusDeleting    = "deleting"
	StatusError       = "error"
	StatusUnreachable = "unreachable"
	StatusArchived    = "archived"
)

func NeedsRecovery(status string) bool {
	return status != StatusStopped && status != StatusError
}

// NeedsRecoveryOnReconnect returns true for statuses that should be reconciled
// when a worker comes back online — includes unreachable gameservers.
func NeedsRecoveryOnReconnect(status string) bool {
	return NeedsRecovery(status) || status == StatusUnreachable
}

func IsRunningStatus(status string) bool {
	return status == StatusRunning
}

// IsPollableStatus returns true if a gameserver is in a state where polling should continue.
// This is more permissive than IsRunningStatus — it includes transitional states like
// "installing" and "starting" because pollers are started early and must survive until
// the gameserver reaches a terminal state.
func IsPollableStatus(status string) bool {
	return status != StatusStopped && status != StatusStopping && status != StatusError
}

// Instance contract constants.
const (
	PortNameQuery = "query" // Port used for server query polling (A2S/GJQ)
	PortNameGame  = "game"  // Fallback port for query polling if no "query" port defined
)

// Disabled capability names — used in game definitions to opt out of features.
const CapabilityQuery = "query"
