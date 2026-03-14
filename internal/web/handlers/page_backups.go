package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageBackupHandlers struct {
	backupSvc     *service.BackupService
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageBackupHandlers(backupSvc *service.BackupService, gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageBackupHandlers {
	return &PageBackupHandlers{backupSvc: backupSvc, gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageBackupHandlers) List(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for backups", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for backups", "game_id", gs.GameID, "error", err)
	}

	backups, err := h.backupSvc.ListBackups(id)
	if err != nil {
		h.log.Error("listing backups", "gameserver_id", id, "error", err)
		http.Error(w, "Failed to load backups", http.StatusInternalServerError)
		return
	}
	if backups == nil {
		backups = []models.Backup{}
	}

	h.renderer.Render(w, r, "gameservers/backups", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"Backups":    backups,
	})
}

func (h *PageBackupHandlers) Create(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	name := ""
	if err := r.ParseForm(); err == nil {
		name = r.FormValue("name")
	}

	// Detach from request context so the backup isn't killed when the HTTP response completes
	if _, err := h.backupSvc.CreateBackup(context.WithoutCancel(r.Context()), id, name); err != nil {
		h.log.Error("creating backup from web", "gameserver_id", id, "error", err)
		http.Error(w, "Failed to create backup: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, id)
}

func (h *PageBackupHandlers) Restore(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backupID := chi.URLParam(r, "backupId")

	if err := h.backupSvc.RestoreBackup(context.WithoutCancel(r.Context()), backupID); err != nil {
		h.log.Error("restoring backup from web", "backup_id", backupID, "error", err)
		http.Error(w, "Failed to restore backup: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, gsID)
}

func (h *PageBackupHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backupID := chi.URLParam(r, "backupId")

	if err := h.backupSvc.DeleteBackup(r.Context(), backupID); err != nil {
		h.log.Error("deleting backup from web", "backup_id", backupID, "error", err)
		http.Error(w, "Failed to delete backup: "+err.Error(), http.StatusBadRequest)
		return
	}

	h.renderList(w, r, gsID)
}

func (h *PageBackupHandlers) renderList(w http.ResponseWriter, r *http.Request, gsID string) {
	w.Header().Set("HX-Push-Url", "false")
	gs, err := h.gameserverSvc.GetGameserver(gsID)
	if err != nil {
		h.log.Error("getting gameserver for backups", "id", gsID, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for backups", "game_id", gs.GameID, "error", err)
	}

	backups, err := h.backupSvc.ListBackups(gsID)
	if err != nil {
		h.log.Error("listing backups", "gameserver_id", gsID, "error", err)
		http.Error(w, "Failed to load backups", http.StatusInternalServerError)
		return
	}
	if backups == nil {
		backups = []models.Backup{}
	}

	h.renderer.Render(w, r, "gameservers/backups", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"Backups":    backups,
	})
}
