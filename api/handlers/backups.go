package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
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
	backups, err := h.svc.ListBackups(models.BackupFilter{
		GameserverID: gsID,
		Pagination:   parsePagination(r),
	})
	if err != nil {
		h.log.Error("listing backups", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
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
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}

	backup, err := h.svc.CreateBackup(detachedCtx(r), gsID, req.Name)
	if err != nil {
		h.log.Error("creating backup", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondAccepted(w, backup)
}

func (h *BackupHandlers) Restore(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backupID := chi.URLParam(r, "backupId")
	if err := h.svc.RestoreBackup(detachedCtx(r), gsID, backupID); err != nil {
		h.log.Error("restoring backup", "backup_id", backupID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	backup, _ := h.svc.GetBackup(gsID, backupID)
	respondAccepted(w, backup)
}

func (h *BackupHandlers) Download(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backupID := chi.URLParam(r, "backupId")

	reader, backup, err := h.svc.DownloadBackup(r.Context(), gsID, backupID)
	if err != nil {
		h.log.Error("downloading backup", "backup_id", backupID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	defer reader.Close()

	filename := fmt.Sprintf("%s.tar.gz", backup.Name)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	if backup.SizeBytes > 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", backup.SizeBytes))
	}

	if _, err := io.Copy(w, reader); err != nil {
		h.log.Error("streaming backup download", "backup_id", backupID, "error", err)
	}
}

func (h *BackupHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	backupID := chi.URLParam(r, "backupId")
	if err := h.svc.DeleteBackup(r.Context(), gsID, backupID); err != nil {
		h.log.Error("deleting backup", "backup_id", backupID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondNoContent(w)
}
