package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/warsmite/gamejanitor/controller/mod"
)

type ModHandlers struct {
	svc *mod.ModService
	log *slog.Logger
}

func NewModHandlers(svc *mod.ModService, log *slog.Logger) *ModHandlers {
	return &ModHandlers{svc: svc, log: log}
}

func (h *ModHandlers) Categories(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	cats, err := h.svc.GetCategories(r.Context(), gsID)
	if err != nil {
		h.log.Error("getting mod categories", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, cats)
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

	results, total, err := h.svc.Search(r.Context(), gsID, category, query, offset, limit)
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

	versions, err := h.svc.GetVersions(r.Context(), gsID, category, source, sourceID)
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

	result, err := h.svc.InstallPack(r.Context(), gsID, req.Source, req.PackID, req.VersionID)
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
