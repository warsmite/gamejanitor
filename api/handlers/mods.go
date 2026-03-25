package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/warsmite/gamejanitor/constants"
	"github.com/warsmite/gamejanitor/service"
)

type ModHandlers struct {
	svc *service.ModService
	log *slog.Logger
}

func NewModHandlers(svc *service.ModService, log *slog.Logger) *ModHandlers {
	return &ModHandlers{svc: svc, log: log}
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

func (h *ModHandlers) Sources(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	sources, err := h.svc.GetSources(r.Context(), gsID)
	if err != nil {
		h.log.Error("getting mod sources", "gameserver_id", gsID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondOK(w, sources)
}

func (h *ModHandlers) Search(w http.ResponseWriter, r *http.Request) {
	gsID := chi.URLParam(r, "id")
	q := r.URL.Query()

	source := q.Get("source")
	if source == "" {
		respondError(w, http.StatusBadRequest, "source parameter is required")
		return
	}

	query := q.Get("q")
	offset, _ := strconv.Atoi(q.Get("offset"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = constants.PaginationDefaultModLimit
	}

	results, total, err := h.svc.Search(r.Context(), gsID, source, query, offset, limit)
	if err != nil {
		h.log.Error("searching mods", "gameserver_id", gsID, "source", source, "query", query, "error", err)
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

	source := q.Get("source")
	sourceID := q.Get("source_id")
	if source == "" || sourceID == "" {
		respondError(w, http.StatusBadRequest, "source and source_id parameters are required")
		return
	}

	versions, err := h.svc.GetVersions(r.Context(), gsID, source, sourceID)
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
		Source    string `json:"source"`
		SourceID  string `json:"source_id"`
		VersionID string `json:"version_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if req.Source == "" || req.SourceID == "" {
		respondError(w, http.StatusBadRequest, "source and source_id are required")
		return
	}

	mod, err := h.svc.Install(r.Context(), gsID, req.Source, req.SourceID, req.VersionID, req.Name)
	if err != nil {
		h.log.Error("installing mod", "gameserver_id", gsID, "source", req.Source, "source_id", req.SourceID, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	respondCreated(w, mod)
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
