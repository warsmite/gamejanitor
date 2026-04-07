package gameserver

import (
	"context"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/model"
)

const maxAutoRestartAttempts = 3

// handleUnexpectedDeath handles an unexpected instance death. If auto-restart
// is enabled and the crash limit hasn't been reached, restarts the gameserver.
func (m *StatusManager) handleUnexpectedDeath(gs *model.Gameserver, reason string) {
	if gs.AutoRestart == nil || !*gs.AutoRestart || m.restartFunc == nil {
		m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, gs.ID, &event.ErrorData{Reason: reason}))
		return
	}

	m.crashMu.Lock()
	m.crashCounts[gs.ID]++
	count := m.crashCounts[gs.ID]
	m.crashMu.Unlock()

	if count > maxAutoRestartAttempts {
		m.log.Error("auto-restart limit reached, giving up", "gameserver", gs.ID, "attempts", maxAutoRestartAttempts, "reason", reason)
		gs.DesiredState = "stopped"
		m.store.UpdateGameserver(gs)
		m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, gs.ID, &event.ErrorData{Reason: fmt.Sprintf("Crashed %d times, auto-restart disabled. Last crash: %s", maxAutoRestartAttempts, reason)}))
		return
	}

	m.log.Warn("auto-restarting crashed gameserver", "gameserver", gs.ID, "attempt", count, "max", maxAutoRestartAttempts)

	// Clear error state so DeriveStatus doesn't block on the previous crash
	m.workerStateMu.Lock()
	delete(m.errorReasons, gs.ID)
	delete(m.workerStates, gs.ID)
	m.workerStateMu.Unlock()

	if m.runner != nil {
		if err := m.runner.Submit(gs.ID, model.OpStart, event.Actor{Type: "system"}, func(ctx context.Context, _ ProgressFunc) error {
			return m.restartFunc(ctx, gs.ID)
		}); err != nil {
			m.log.Error("auto-restart rejected by operation guard", "gameserver", gs.ID, "attempt", count, "error", err)
		}
	} else {
		go func() {
			if err := m.restartFunc(context.Background(), gs.ID); err != nil {
				m.log.Error("auto-restart failed", "gameserver", gs.ID, "attempt", count, "error", err)
				m.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, gs.ID, &event.ErrorData{Reason: fmt.Sprintf("Auto-restart failed (attempt %d/%d): %s", count, maxAutoRestartAttempts, err.Error())}))
			}
		}()
	}
}

// describeExit produces a human-readable crash reason from the exit code,
// uptime, and last-known resource usage.
func describeExit(exitCode int, uptime time.Duration, lastStats *event.StatsData) string {
	uptimeStr := uptime.Round(time.Second).String()

	var reason string
	switch exitCode {
	case 137:
		reason = "Killed by system (out of memory)"
		if lastStats != nil && lastStats.MemoryLimitMB > 0 {
			pct := float64(lastStats.MemoryUsageMB) / float64(lastStats.MemoryLimitMB) * 100
			reason = fmt.Sprintf("Killed by system (out of memory — was using %d/%d MB, %.0f%%). Increase memory limit.", lastStats.MemoryUsageMB, lastStats.MemoryLimitMB, pct)
		}
	case 139:
		reason = "Crashed (segmentation fault)"
	case 143:
		reason = "Terminated by signal"
	case -1:
		reason = "Killed by signal"
	case 1:
		reason = "Server exited with error (exit code 1). Check console for details."
	case 2:
		reason = "Server exited (interrupted)"
	default:
		if exitCode > 128 {
			reason = fmt.Sprintf("Killed by signal %d", exitCode-128)
		} else {
			reason = fmt.Sprintf("Server exited with code %d. Check console for details.", exitCode)
		}
	}

	return fmt.Sprintf("%s (after %s)", reason, uptimeStr)
}
