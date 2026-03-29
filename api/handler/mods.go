package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/model"
)

type ModHandlers struct {
	svc *mod.ModService
	log *slog.Logger
}

func NewModHandlers(svc *mod.ModService, log *slog.Logger) *ModHandlers {
	return &ModHandlers{svc: svc, log: log}
}

func (h *ModHandlers) Config(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	config, err := h.svc.GetConfig(r.Context(), gsID)
	if err != nil {
		h.log.Error("getting mod config", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, config)
}

func (h *ModHandlers) List(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	mods, err := h.svc.ListInstalled(r.Context(), gsID)
	if err != nil {
		h.log.Error("listing installed mods", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, mods)
}

func (h *ModHandlers) Search(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	q := r.URL.Query()

	category := q.Get("category")
	if category == "" {
		respondError(w, http.StatusBadRequest, "category parameter is required")
		return
	}

	query := q.Get("q")
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = PaginationDefaultModLimit
	}

	opts := mod.SearchOptions{
		Version: q.Get("version"),
		Loader:  q.Get("loader"),
		Sort:    q.Get("sort"),
	}

	results, total, err := h.svc.Search(r.Context(), gsID, category, query, opts, offset, limit)
	if err != nil {
		h.log.Error("searching mods", "gameserver_id", gsID, "category", category, "query", query, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondOK(w, map[string]any{
		"results": results,
		"total":   total,
		"offset":  offset,
		"limit":   limit,
	})
}

func (h *ModHandlers) Versions(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	q := r.URL.Query()

	category := q.Get("category")
	source := q.Get("source")
	sourceID := q.Get("source_id")
	if category == "" || source == "" || sourceID == "" {
		respondError(w, http.StatusBadRequest, "category, source, and source_id parameters are required")
		return
	}

	unfiltered := q.Get("unfiltered") == "true"
	versions, err := h.svc.GetVersions(r.Context(), gsID, category, source, sourceID, unfiltered)
	if err != nil {
		h.log.Error("getting mod versions", "gameserver_id", gsID, "source", source, "source_id", sourceID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, versions)
}

func (h *ModHandlers) Install(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	var req struct {
		Category  string `json:"category"`
		Source    string `json:"source"`
		SourceID  string `json:"source_id"`
		VersionID string `json:"version_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Category == "" || req.Source == "" || req.SourceID == "" {
		respondError(w, http.StatusBadRequest, "category, source, and source_id are required")
		return
	}

	installed, err := h.svc.Install(r.Context(), gsID, req.Category, req.Source, req.SourceID, req.VersionID)
	if err != nil {
		h.log.Error("installing mod", "gameserver_id", gsID, "source", req.Source, "source_id", req.SourceID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondCreated(w, installed)
}

func (h *ModHandlers) InstallPack(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	var req struct {
		Source    string `json:"source"`
		PackID    string `json:"pack_id"`
		VersionID string `json:"version_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Source == "" || req.PackID == "" {
		respondError(w, http.StatusBadRequest, "source and pack_id are required")
		return
	}

	// Detach from HTTP request context — modpack installs can take minutes
	// downloading hundreds of mods and must not cancel when the request ends.
	result, err := h.svc.InstallPack(context.Background(), gsID, req.Source, req.PackID, req.VersionID)
	if err != nil {
		h.log.Error("installing modpack", "gameserver_id", gsID, "source", req.Source, "pack_id", req.PackID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondCreated(w, result)
}

func (h *ModHandlers) Uninstall(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	modID := chi.URLParam(r, "modId")

	if err := h.svc.Uninstall(r.Context(), gsID, modID); err != nil {
		h.log.Error("uninstalling mod", "gameserver_id", gsID, "mod_id", modID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondNoContent(w)
}

func (h *ModHandlers) CheckUpdates(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	updates, err := h.svc.CheckForUpdates(r.Context(), gsID)
	if err != nil {
		h.log.Error("checking mod updates", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if updates == nil {
		updates = []mod.ModUpdate{}
	}
	respondOK(w, updates)
}

func (h *ModHandlers) Update(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	modID := chi.URLParam(r, "modId")

	updated, err := h.svc.Update(r.Context(), gsID, modID)
	if err != nil {
		h.log.Error("updating mod", "gameserver_id", gsID, "mod_id", modID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, updated)
}

func (h *ModHandlers) UpdateAll(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	updates, err := h.svc.UpdateAll(r.Context(), gsID)
	if err != nil {
		h.log.Error("updating all mods", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, updates)
}

func (h *ModHandlers) UpdatePack(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	modID := chi.URLParam(r, "modId")

	result, err := h.svc.UpdatePack(r.Context(), gsID, modID)
	if err != nil {
		h.log.Error("updating modpack", "gameserver_id", gsID, "mod_id", modID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, result)
}

func (h *ModHandlers) CheckCompatibility(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	var req struct {
		Env map[string]string `json:"env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	env := make(model.Env, len(req.Env))
	for k, v := range req.Env {
		env[k] = v
	}

	issues, err := h.svc.CheckCompatibility(r.Context(), gsID, env)
	if err != nil {
		h.log.Error("checking mod compatibility", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	if issues == nil {
		issues = []mod.ModIssue{}
	}
	respondOK(w, issues)
}

func (h *ModHandlers) Scan(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	result, err := h.svc.Scan(r.Context(), gsID)
	if err != nil {
		h.log.Error("scan failed", "gameserver", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, result)
}

func (h *ModHandlers) TrackFile(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	var body struct {
		Category string `json:"category"`
		Name     string `json:"name"`
		Path     string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Path == "" {
		respondError(w, http.StatusBadRequest, "path is required")
		return
	}

	mod, err := h.svc.TrackFile(r.Context(), gsID, body.Category, body.Path, body.Name)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondCreated(w, mod)
}

func (h *ModHandlers) InstallURL(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	var body struct {
		Category string `json:"category"`
		Name     string `json:"name"`
		URL      string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.URL == "" {
		respondError(w, http.StatusBadRequest, "url is required")
		return
	}
	if body.Name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}

	mod, err := h.svc.InstallFromURL(r.Context(), gsID, body.Category, body.Name, body.URL)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondCreated(w, mod)
}

func (h *ModHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")

	if err := r.ParseMultipartForm(50 << 20); err != nil {
		respondError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}

	category := r.FormValue("category")
	name := r.FormValue("name")
	if name == "" {
		respondError(w, http.StatusBadRequest, "name is required")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respondError(w, http.StatusBadRequest, "file is required")
		return
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, 50<<20))
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read file")
		return
	}

	mod, err := h.svc.InstallFromUpload(r.Context(), gsID, category, name, header.Filename, content)
	if err != nil {
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondCreated(w, mod)
}
