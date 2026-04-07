package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
	"github.com/warsmite/gamejanitor/model"
	"github.com/go-chi/chi/v5"
)

func (h *GameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	actor := event.ActorFromContext(r.Context())
	if err := h.ops.Submit(id, model.OpDelete, actor, func(ctx context.Context, _ operation.ProgressFunc) error {
		return h.svc.DeleteGameserver(ctx, id)
	}); err != nil {
		h.log.Error("deleting gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	gs, _ := h.svc.GetGameserver(id)
	respondAccepted(w, gs)
}

func (h *GameserverHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpStart, func(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
		return h.lifecycle.Start(ctx, id, onProgress)
	})
}

func (h *GameserverHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpStop, func(ctx context.Context, id string, _ operation.ProgressFunc) error {
		return h.lifecycle.Stop(ctx, id)
	})
}

func (h *GameserverHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpRestart, func(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
		return h.lifecycle.Restart(ctx, id, onProgress)
	})
}

func (h *GameserverHandlers) UpdateServerGame(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpUpdate, func(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
		return h.lifecycle.UpdateServerGame(ctx, id, onProgress)
	})
}

func (h *GameserverHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpReinstall, func(ctx context.Context, id string, onProgress operation.ProgressFunc) error {
		return h.lifecycle.Reinstall(ctx, id, onProgress)
	})
}

func (h *GameserverHandlers) Archive(w http.ResponseWriter, r *http.Request) {
	h.submitAction(w, r, model.OpArchive, func(ctx context.Context, id string, _ operation.ProgressFunc) error {
		return h.lifecycle.Archive(ctx, id)
	})
}

func (h *GameserverHandlers) Unarchive(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body struct {
		NodeID string `json:"node_id"`
	}
	// Body is optional — empty body means auto-place
	json.NewDecoder(r.Body).Decode(&body)

	actor := event.ActorFromContext(r.Context())
	if err := h.ops.Submit(id, model.OpUnarchive, actor, func(ctx context.Context, _ operation.ProgressFunc) error {
		return h.lifecycle.Unarchive(ctx, id, body.NodeID)
	}); err != nil {
		h.log.Error("unarchiving gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	gs, _ := h.svc.GetGameserver(id)
	respondAccepted(w, gs)
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

	actor := event.ActorFromContext(r.Context())
	if err := h.ops.Submit(id, model.OpMigrate, actor, func(ctx context.Context, onProgress operation.ProgressFunc) error {
		return h.lifecycle.MigrateGameserver(ctx, id, body.NodeID, onProgress)
	}); err != nil {
		h.log.Error("migrating gameserver", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	gs, _ := h.svc.GetGameserver(id)
	respondAccepted(w, gs)
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

	type bulkActionDef struct {
		opType string
		fn     func(context.Context, string, operation.ProgressFunc) error
	}
	actionDefs := map[string]bulkActionDef{
		"start":   {model.OpStart, h.lifecycle.Start},
		"stop":    {model.OpStop, func(ctx context.Context, id string, _ operation.ProgressFunc) error { return h.lifecycle.Stop(ctx, id) }},
		"restart": {model.OpRestart, h.lifecycle.Restart},
	}

	def, ok := actionDefs[body.Action]
	if !ok {
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

	gameservers, err := h.svc.ListGameservers(r.Context(), filter)
	if err != nil {
		h.log.Error("listing gameservers for bulk action", "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	actor := event.ActorFromContext(r.Context())
	type result struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	var results []result
	for _, gs := range gameservers {
		res := result{ID: gs.ID, Name: gs.Name}
		gsID := gs.ID
		if err := h.ops.Submit(gsID, def.opType, actor, func(ctx context.Context, onProgress operation.ProgressFunc) error {
			return def.fn(ctx, gsID, onProgress)
		}); err != nil {
			res.Error = err.Error()
			res.Status = gs.Status
		} else {
			res.Status = "accepted"
		}
		results = append(results, res)
	}

	h.log.Info("bulk action submitted", "action", body.Action, "total", len(gameservers), "node_id", body.NodeID)
	respondOK(w, results)
}

// submitAction submits a lifecycle operation through the runner and returns the gameserver.
// The runner handles context detachment, activity tracking, and the operation guard.
func (h *GameserverHandlers) submitAction(w http.ResponseWriter, r *http.Request, opType string, fn func(context.Context, string, operation.ProgressFunc) error) {
	id := chi.URLParam(r, "id")
	actor := event.ActorFromContext(r.Context())
	if err := h.ops.Submit(id, opType, actor, func(ctx context.Context, onProgress operation.ProgressFunc) error {
		return fn(ctx, id, onProgress)
	}); err != nil {
		h.log.Error("gameserver action failed", "id", id, "operation", opType, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}

	gs, err := h.svc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after action", "id", id, "error", err)
		respondError(w, serviceErrorStatus(err), serviceErrorMessage(err))
		return
	}
	respondAccepted(w, gs)
}
