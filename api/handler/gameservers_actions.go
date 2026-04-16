package handler

import (
	"encoding/json"
	"net/http"

	"github.com/warsmite/gamejanitor/model"
	"github.com/go-chi/chi/v5"
)

func (h *GameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := h.manager.Delete(detachedCtx(r), id); err != nil {
		h.log.Error("deleting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Start(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Start(detachedCtx(r)); err != nil {
		h.log.Error("starting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Stop(detachedCtx(r)); err != nil {
		h.log.Error("stopping gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Restart(detachedCtx(r)); err != nil {
		h.log.Error("restarting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) UpdateServerGame(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.UpdateServerGame(detachedCtx(r)); err != nil {
		h.log.Error("updating game", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Reinstall(detachedCtx(r)); err != nil {
		h.log.Error("reinstalling gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Archive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Archive(detachedCtx(r)); err != nil {
		h.log.Error("archiving gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Unarchive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	json.NewDecoder(r.Body).Decode(&body)

	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Unarchive(detachedCtx(r), body.NodeID); err != nil {
		h.log.Error("unarchiving gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) Migrate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.NodeID == "" {
		respondError(w, http.StatusBadRequest, "node_id is required")
		return
	}

	gs := h.manager.Get(id)
	if gs == nil {
		respondError(w, http.StatusNotFound, "gameserver "+id+" not found")
		return
	}
	if err := gs.Migrate(detachedCtx(r), body.NodeID); err != nil {
		h.log.Error("migrating gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs.Snapshot())
}

func (h *GameserverHandlers) BulkAction(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		NodeID string `json:"node_id"`
		All    bool   `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}

	if body.Action != "start" && body.Action != "stop" && body.Action != "restart" {
		respondError(w, http.StatusBadRequest, "action must be start, stop, or restart")
		return
	}
	if !body.All && body.NodeID == "" {
		respondError(w, http.StatusBadRequest, "either all or node_id is required")
		return
	}

	filter := model.GameserverFilter{}
	if body.NodeID != "" {
		filter.NodeID = &body.NodeID
	}

	gameservers, err := h.manager.List(r.Context(), filter)
	if err != nil {
		h.log.Error("listing gameservers for bulk action", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	type result struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []result
	for _, snap := range gameservers {
		res := result{ID: snap.ID, Name: snap.Name}
		gs := h.manager.Get(snap.ID)
		if gs == nil {
			res.Error = "not found"
			results = append(results, res)
			continue
		}

		var actionErr error
		ctx := detachedCtx(r)
		switch body.Action {
		case "start":
			actionErr = gs.Start(ctx)
		case "stop":
			actionErr = gs.Stop(ctx)
		case "restart":
			actionErr = gs.Restart(ctx)
		}

		if actionErr != nil {
			res.Error = actionErr.Error()
			res.Status = snap.Status
		} else {
			res.Status = "accepted"
		}
		results = append(results, res)
	}

	h.log.Info("bulk action submitted", "action", body.Action, "total", len(gameservers), "node_id", body.NodeID)
	respondOK(w, results)
}
