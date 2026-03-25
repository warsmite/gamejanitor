package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path"

	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/service"
	"github.com/go-chi/chi/v5"
)

type FileHandlers struct {
	svc *service.FileService
	log *slog.Logger
}

func NewFileHandlers(svc *service.FileService, log *slog.Logger) *FileHandlers {
	return &FileHandlers{svc: svc, log: log}
}

func (h *FileHandlers) List(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/data"
	}

	entries, err := h.svc.ListDirectory(r.Context(), id, dirPath)
	if err != nil {
		h.log.Error("listing directory", "gameserver_id", id, "path", dirPath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, entries)
}

func (h *FileHandlers) Read(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	content, err := h.svc.ReadFile(r.Context(), id, filePath)
	if err != nil {
		h.log.Error("reading file", "gameserver_id", id, "path", filePath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, map[string]string{"content": string(content)})
}

func (h *FileHandlers) Write(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	content, err := io.ReadAll(io.LimitReader(r.Body, constants.MaxFileWriteBytes))
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	if err := h.svc.WriteFile(r.Context(), id, filePath, content); err != nil {
		h.log.Error("writing file", "gameserver_id", id, "path", filePath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}

func (h *FileHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	targetPath := r.URL.Query().Get("path")
	if targetPath == "" {
		respondError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	if err := h.svc.DeletePath(r.Context(), id, targetPath); err != nil {
		h.log.Error("deleting path", "gameserver_id", id, "path", targetPath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}

func (h *FileHandlers) Download(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	content, err := h.svc.ReadFile(r.Context(), id, filePath)
	if err != nil {
		h.log.Error("downloading file", "gameserver_id", id, "path", filePath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	filename := path.Base(filePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	w.Write(content)
}

func (h *FileHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/data"
	}

	if err := r.ParseMultipartForm(constants.MaxFileUploadBytes); err != nil {
		respondError(w, http.StatusBadRequest, "failed to parse multipart form")
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		respondError(w, http.StatusBadRequest, "no files provided")
		return
	}

	type uploadedFile struct {
		Path string `json:"path"`
		Size int    `json:"size"`
	}
	var uploaded []uploadedFile

	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			h.log.Error("opening uploaded file", "filename", fh.Filename, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to read uploaded file: "+fh.Filename)
			return
		}

		content, err := io.ReadAll(io.LimitReader(f, constants.MaxFileUploadBytes))
		f.Close()
		if err != nil {
			h.log.Error("reading uploaded file", "filename", fh.Filename, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to read uploaded file: "+fh.Filename)
			return
		}

		destPath := dirPath + "/" + fh.Filename
		if err := h.svc.WriteFile(r.Context(), id, destPath, content); err != nil {
			h.log.Error("writing uploaded file", "gameserver_id", id, "path", destPath, "error", err)
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}

		h.log.Info("file uploaded via API", "gameserver_id", id, "path", destPath, "size", len(content))
		uploaded = append(uploaded, uploadedFile{Path: destPath, Size: len(content)})
	}

	respondOK(w, uploaded)
}

func (h *FileHandlers) Rename(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.From == "" || req.To == "" {
		respondError(w, http.StatusBadRequest, "from and to are required")
		return
	}

	if err := h.svc.RenamePath(r.Context(), id, req.From, req.To); err != nil {
		h.log.Error("renaming path", "gameserver_id", id, "from", req.From, "to", req.To, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}

func (h *FileHandlers) CreateDirectory(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if req.Path == "" {
		respondError(w, http.StatusBadRequest, "path is required")
		return
	}

	if err := h.svc.CreateDirectory(r.Context(), id, req.Path); err != nil {
		h.log.Error("creating directory", "gameserver_id", id, "path", req.Path, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}
