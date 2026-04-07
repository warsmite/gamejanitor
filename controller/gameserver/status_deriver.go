package gameserver

import (
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker"
)

// DeriveStatus computes the user-facing status for a gameserver by combining
// worker-reported state, operation phase, archive flag, and worker reachability.
// Also sets StartedAt from the worker state cache when the instance is running.
// This is the single source of truth — no status column needed.
func (m *StatusManager) DeriveStatus(gs *model.Gameserver) (status string, errorReason string) {
	if gs.DesiredState == "archived" {
		return controller.StatusArchived, ""
	}

	// Check worker reachability
	if gs.NodeID != nil {
		if _, online := m.registry.Get(*gs.NodeID); !online {
			return controller.StatusUnreachable, "Worker offline"
		}
	}

	// Check for controller-side errors (migration failed, backup restore failed, etc.)
	m.workerStateMu.RLock()
	errReason := m.errorReasons[gs.ID]
	m.workerStateMu.RUnlock()
	if errReason != "" {
		return controller.StatusError, errReason
	}

	// Get worker-reported state
	ws := m.getWorkerState(gs.ID)

	// Populate StartedAt from worker state when the instance is running
	if ws != nil && !ws.StartedAt.IsZero() && (ws.State == worker.StateRunning || ws.State == worker.StateStarting) {
		gs.StartedAt = &ws.StartedAt
	}

	// Check operation phase first — operations override worker state display
	if gs.Operation != nil {
		switch gs.Operation.Phase {
		case model.PhasePullingImage, model.PhaseDownloadingGame, model.PhaseInstalling:
			return controller.StatusInstalling, ""
		case model.PhaseStopping:
			return controller.StatusStopping, ""
		}
	}

	if ws == nil {
		// No worker state yet — if desired_state is "running", an async operation
		// is in progress (doStart hasn't created the instance yet)
		if gs.DesiredState == "running" {
			return controller.StatusStarting, ""
		}
		return controller.StatusStopped, ""
	}

	switch ws.State {
	case worker.StateCreated, worker.StateStarting:
		return controller.StatusStarting, ""
	case worker.StateRunning:
		return controller.StatusRunning, ""
	case worker.StateExited:
		if ws.ExitCode == 0 {
			return controller.StatusStopped, ""
		}
		return controller.StatusError, "Instance exited unexpectedly"
	}

	return controller.StatusStopped, ""
}
