package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"

	"github.com/warsmite/gamejanitor/controller/file"
	"github.com/go-chi/chi/v5"
)

type FileHandlers struct {
	svc *file.Service
	log *slog.Logger
}

func NewFileHandlers(svc *file.Service, log *slog.Logger) *FileHandlers {
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
		h.log.Error("listing directory", "gameserver", id, "path", dirPath, "error", err)
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

	// Check file size before reading into memory
	reader, size, err := h.svc.OpenFile(r.Context(), id, filePath)
	if err != nil {
		h.log.Error("reading file", "gameserver", id, "path", filePath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	reader.Close()

	if size > MaxFileReadBytes {
		respondError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file is %d MB, exceeds %d MB read limit — use the download endpoint for large files", size/(1024*1024), MaxFileReadBytes/(1024*1024)))
		return
	}

	content, err := h.svc.ReadFile(r.Context(), id, filePath)
	if err != nil {
		h.log.Error("reading file", "gameserver", id, "path", filePath, "error", err)
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

	content, err := io.ReadAll(io.LimitReader(r.Body, MaxFileWriteBytes+1))
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	if int64(len(content)) > MaxFileWriteBytes {
		respondError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("file exceeds %d MB limit, use SFTP for large files", MaxFileWriteBytes/(1024*1024)))
		return
	}

	if err := h.svc.WriteFile(r.Context(), id, filePath, content); err != nil {
		h.log.Error("writing file", "gameserver", id, "path", filePath, "error", err)
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
		h.log.Error("deleting path", "gameserver", id, "path", targetPath, "error", err)
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

	reader, size, err := h.svc.OpenFile(r.Context(), id, filePath)
	if err != nil {
		h.log.Error("downloading file", "gameserver", id, "path", filePath, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	defer reader.Close()

	filename := path.Base(filePath)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(filename)))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	io.Copy(w, reader)
}

func (h *FileHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/data"
	}

	// Keep at most 2MB in memory; the rest spills to temp files on disk.
	// Individual files are still capped at MaxFileUploadBytes when read.
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
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
		if fh.Size > MaxFileUploadBytes {
			respondError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("%s exceeds %d MB limit, use SFTP for large files", fh.Filename, MaxFileUploadBytes/(1024*1024)))
			return
		}

		f, err := fh.Open()
		if err != nil {
			h.log.Error("opening uploaded file", "filename", fh.Filename, "error", err)
			respondError(w, http.StatusInternalServerError, "failed to read uploaded file: "+fh.Filename)
			return
		}

		destPath := dirPath + "/" + fh.Filename
		if err := h.svc.WriteFileStream(r.Context(), id, destPath, io.LimitReader(f, MaxFileUploadBytes), 0644); err != nil {
			f.Close()
			h.log.Error("writing uploaded file", "gameserver", id, "path", destPath, "error", err)
			respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
			return
		}
		f.Close()

		h.log.Info("file uploaded via API", "gameserver", id, "path", destPath, "size", fh.Size)
		uploaded = append(uploaded, uploadedFile{Path: destPath, Size: int(fh.Size)})
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
		h.log.Error("renaming path", "gameserver", id, "from", req.From, "to", req.To, "error", err)
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
		h.log.Error("creating directory", "gameserver", id, "path", req.Path, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}
