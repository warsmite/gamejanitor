package service

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
)

type StatusManager struct {
	db     *sql.DB
	docker *docker.Client
	log    *slog.Logger
	cancel context.CancelFunc
}

func NewStatusManager(db *sql.DB, dockerClient *docker.Client, log *slog.Logger) *StatusManager {
	return &StatusManager{db: db, docker: dockerClient, log: log}
}

// Start begins watching Docker events and updating gameserver status.
func (m *StatusManager) Start(ctx context.Context) {
	ctx, m.cancel = context.WithCancel(ctx)

	eventCh, errCh := m.docker.WatchEvents(ctx)

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
// Returns a list of gameserver IDs that should be auto-started.
func (m *StatusManager) RecoverOnStartup(ctx context.Context) ([]string, error) {
	m.log.Info("recovering gameserver status from docker state")

	gameservers, err := models.ListGameservers(m.db, models.GameserverFilter{})
	if err != nil {
		return nil, err
	}

	var autoStartIDs []string

	for _, gs := range gameservers {
		if isNonTerminalStatus(gs.Status) {
			m.recoverGameserver(ctx, &gs)
		}

		// Re-read after potential status change
		updated, err := models.GetGameserver(m.db, gs.ID)
		if err != nil {
			m.log.Error("failed to re-read gameserver after recovery", "id", gs.ID, "error", err)
			continue
		}
		if updated != nil && updated.AutoStart && updated.Status == "stopped" {
			autoStartIDs = append(autoStartIDs, updated.ID)
		}
	}

	return autoStartIDs, nil
}

func (m *StatusManager) recoverGameserver(ctx context.Context, gs *models.Gameserver) {
	if gs.ContainerID == nil {
		m.log.Info("gameserver has no container, setting stopped", "id", gs.ID, "was_status", gs.Status)
		m.updateStatus(gs, "stopped")
		return
	}

	info, err := m.docker.InspectContainer(ctx, *gs.ContainerID)
	if err != nil {
		m.log.Warn("container not found, setting stopped", "id", gs.ID, "container_id", (*gs.ContainerID)[:12], "error", err)
		gs.ContainerID = nil
		m.updateStatus(gs, "stopped")
		return
	}

	switch info.State {
	case "running":
		m.log.Info("container is running, setting running", "id", gs.ID)
		m.updateStatus(gs, "running")
	case "exited", "dead", "created":
		m.log.Info("container is not running, setting stopped", "id", gs.ID, "state", info.State)
		gs.ContainerID = nil
		m.updateStatus(gs, "stopped")
	default:
		m.log.Warn("container in unexpected state, setting error", "id", gs.ID, "state", info.State)
		m.updateStatus(gs, "error")
	}
}

func (m *StatusManager) handleEvent(event docker.ContainerEvent) {
	// Extract gameserver ID from container name "gamejanitor-<id>"
	gsID := strings.TrimPrefix(event.ContainerName, "gamejanitor-")
	if gsID == event.ContainerName {
		return
	}
	// Skip temp containers (update/reinstall)
	if strings.Contains(gsID, "-update-") || strings.Contains(gsID, "-reinstall-") {
		return
	}

	gs, err := models.GetGameserver(m.db, gsID)
	if err != nil || gs == nil {
		m.log.Debug("docker event for unknown gameserver", "container_name", event.ContainerName, "action", event.Action)
		return
	}

	switch event.Action {
	case "start":
		// Container started — lifecycle service handles status transitions,
		// so we only log here to avoid conflicting updates
		m.log.Debug("docker event: container started", "id", gsID)

	case "die", "stop":
		if gs.Status == "stopping" {
			// Expected stop — lifecycle service handles the transition
			m.log.Debug("docker event: expected container stop", "id", gsID)
		} else if gs.Status == "running" || gs.Status == "started" {
			// Unexpected death
			m.log.Warn("docker event: unexpected container death", "id", gsID, "status", gs.Status, "action", event.Action)
			m.updateStatus(gs, "error")
		}

	case "kill":
		m.log.Debug("docker event: container killed", "id", gsID)
	}
}

func (m *StatusManager) updateStatus(gs *models.Gameserver, newStatus string) {
	oldStatus := gs.Status
	gs.Status = newStatus
	if err := models.UpdateGameserver(m.db, gs); err != nil {
		m.log.Error("failed to update gameserver status", "id", gs.ID, "from", oldStatus, "to", newStatus, "error", err)
		return
	}
	m.log.Info("gameserver status changed", "id", gs.ID, "from", oldStatus, "to", newStatus)
}

func isNonTerminalStatus(status string) bool {
	switch status {
	case "pulling", "starting", "started", "running", "stopping":
		return true
	}
	return false
}
