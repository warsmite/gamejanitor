package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type BackupHandlers struct {
	svc *service.BackupService
	log *slog.Logger
}

func NewBackupHandlers(svc *service.BackupService, log *slog.Logger) *BackupHandlers {
	return &BackupHandlers{svc: svc, log: log}
}

func (h *BackupHandlers) List(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backups, err := h.svc.ListBackups(gsID)
	if err != nil {
		h.log.Error("listing backups", "gameserver_id", gsID, "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if backups == nil {
		backups = []models.Backup{}
	}
	respondOK(w, backups)
}

func (h *BackupHandlers) Create(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	backup, err := h.svc.CreateBackup(context.WithoutCancel(r.Context()), gsID, req.Name)
	if err != nil {
		h.log.Error("creating backup", "gameserver_id", gsID, "error", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	respondCreated(w, backup)
}

func (h *BackupHandlers) Restore(w http.ResponseWriter, r *http.Request) {
	backupID := chi.URLParam(r, "backupId")
	if err := h.svc.RestoreBackup(context.WithoutCancel(r.Context()), backupID); err != nil {
		h.log.Error("restoring backup", "backup_id", backupID, "error", err)
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondOK(w, map[string]string{"status": "restored"})
}

func (h *BackupHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	backupID := chi.URLParam(r, "backupId")
	if err := h.svc.DeleteBackup(r.Context(), backupID); err != nil {
		h.log.Error("deleting backup", "backup_id", backupID, "error", err)
		respondError(w, serviceErrorStatus(err), err.Error())
		return
	}
	respondNoContent(w)
}
