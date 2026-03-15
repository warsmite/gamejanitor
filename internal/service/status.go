package service

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
)

type StatusManager struct {
	db           *sql.DB
	worker       worker.Worker
	log          *slog.Logger
	broadcaster  *EventBroadcaster
	querySvc     *QueryService
	readyWatcher *ReadyWatcher
	cancel       context.CancelFunc
}

func NewStatusManager(db *sql.DB, w worker.Worker, broadcaster *EventBroadcaster, querySvc *QueryService, readyWatcher *ReadyWatcher, log *slog.Logger) *StatusManager {
	return &StatusManager{db: db, worker: w, broadcaster: broadcaster, querySvc: querySvc, readyWatcher: readyWatcher, log: log}
}

// Start begins watching Docker events and updating gameserver status.
func (m *StatusManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	eventCh, errCh := m.worker.WatchEvents(ctx)

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errCh:
				if !ok {
					return
				}
				m.log.Error("docker event watcher error", "error", err)
				return
			case event, ok := <-eventCh:
				if !ok {
					return
				}
				m.handleEvent(event)
			}
		}
	}()

	m.log.Info("status manager started")
}

func (m *StatusManager) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	m.log.Info("status manager stopped")
}

// RecoverOnStartup reconciles DB status with Docker reality.
// Any gameserver not in a terminal state (stopped/error) is checked against
// the actual Docker container and corrected.
func (m *StatusManager) RecoverOnStartup(ctx context.Context) error {
	m.log.Info("recovering gameserver status from docker state")

	gameservers, err := models.ListGameservers(m.db, models.GameserverFilter{})
	if err != nil {
		return err
	}

	for _, gs := range gameservers {
		if needsRecovery(gs.Status) {
			m.recoverGameserver(ctx, &gs)
		}
	}

	return nil
}

func (m *StatusManager) recoverGameserver(ctx context.Context, gs *models.Gameserver) {
	if gs.ContainerID == nil {
		m.log.Info("gameserver has no container, setting stopped", "id", gs.ID, "was_status", gs.Status)
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusStopped, "")
		return
	}

	info, err := m.worker.InspectContainer(ctx, *gs.ContainerID)
	if err != nil {
		m.log.Warn("container not found, setting stopped", "id", gs.ID, "container_id", (*gs.ContainerID)[:12], "error", err)
		m.clearContainerAndSetStatus(gs, StatusStopped)
		return
	}

	switch info.State {
	case "running":
		uptime := time.Since(info.StartedAt)
		if uptime > 60*time.Second {
			// Running for over a minute — assume ready, promote directly and start query polling
			m.log.Info("container running >60s, promoting to running", "id", gs.ID, "uptime", uptime)
			setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusRunning, "")
			m.querySvc.StartPolling(gs.ID)
		} else {
			// Recently started — use ReadyWatcher to detect ready pattern
			m.log.Info("container recently started, watching for ready pattern", "id", gs.ID, "uptime", uptime)
			setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusStarted, "")
			m.readyWatcher.Watch(gs.ID, m.worker, *gs.ContainerID)
		}
	case "exited", "dead", "created":
		m.log.Info("container is not running, setting stopped", "id", gs.ID, "state", info.State)
		m.clearContainerAndSetStatus(gs, StatusStopped)
	default:
		m.log.Warn("container in unexpected state, setting error", "id", gs.ID, "state", info.State)
		setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError, "Container found in unexpected state.")
	}
}

// clearContainerAndSetStatus clears the container_id and updates status in one DB write.
func (m *StatusManager) clearContainerAndSetStatus(gs *models.Gameserver, newStatus string) {
	oldStatus := gs.Status
	gs.ContainerID = nil
	gs.Status = newStatus
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(m.db, gs); err != nil {
		m.log.Error("failed to clear container and update status", "id", gs.ID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("gameserver status changed", "id", gs.ID, "from", oldStatus, "to", newStatus)

	if m.broadcaster != nil {
		m.broadcaster.Publish(StatusEvent{
			GameserverID: gs.ID,
			OldStatus:    oldStatus,
			NewStatus:    newStatus,
			Timestamp:    time.Now(),
		})
	}
}

func (m *StatusManager) handleEvent(event worker.ContainerEvent) {
	gsID := strings.TrimPrefix(event.ContainerName, "gamejanitor-")
	if gsID == event.ContainerName {
		return
	}
	// Skip temp containers (update/reinstall/backup/fileops)
	if strings.Contains(gsID, "-update-") || strings.Contains(gsID, "-reinstall-") || strings.Contains(gsID, "-backup-") || strings.Contains(gsID, "-fileops-") {
		return
	}

	gs, err := models.GetGameserver(m.db, gsID)
	if err != nil || gs == nil {
		m.log.Debug("docker event for unknown gameserver", "container_name", event.ContainerName, "action", event.Action)
		return
	}

	switch event.Action {
	case "start":
		m.log.Debug("docker event: container started", "id", gsID)

	case "die", "stop":
		m.readyWatcher.Stop(gsID)
		m.querySvc.StopPolling(gsID)
		if gs.Status == StatusStopping {
			m.log.Debug("docker event: expected container stop", "id", gsID)
		} else if gs.Status == StatusRunning || gs.Status == StatusStarted {
			m.log.Warn("docker event: unexpected container death", "id", gsID, "status", gs.Status, "action", event.Action)
			setGameserverStatus(m.db, m.log, m.broadcaster, gs.ID, StatusError, "Gameserver stopped unexpectedly.")
		}

	case "kill":
		m.log.Debug("docker event: container killed", "id", gsID)
	}
}
