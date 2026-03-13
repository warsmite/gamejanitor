package handlers

import (
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageActionHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageActionHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageActionHandlers {
	return &PageActionHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageActionHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.gameserverSvc.Start(r.Context(), id) })
}

func (h *PageActionHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.gameserverSvc.Stop(r.Context(), id) })
}

func (h *PageActionHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.gameserverSvc.Restart(r.Context(), id) })
}

func (h *PageActionHandlers) UpdateGame(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.gameserverSvc.UpdateServerGame(r.Context(), id) })
}

func (h *PageActionHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(id string) error { return h.gameserverSvc.Reinstall(r.Context(), id) })
}

// doAction runs a lifecycle action, then returns the updated gameserver card or detail page.
func (h *PageActionHandlers) doAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	id := chi.URLParam(r, "id")
	if err := action(id); err != nil {
		h.log.Error("gameserver page action failed", "id", id, "error", err)
		http.Error(w, "Action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver after action", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game after action", "game_id", gs.GameID, "error", err)
	}

	// If request came from dashboard (targets a card), return card partial
	if r.Header.Get("HX-Target") != "" && r.Header.Get("HX-Target") != "content" {
		view := gameserverView{
			ID:       gs.ID,
			Name:     gs.Name,
			GameID:   gs.GameID,
			GameName: gs.GameID,
			Status:   gs.Status,
			GamePort: firstGamePort(gs.Ports),
		}
		if game != nil {
			view.GameName = game.Name
			view.GridPath = game.GridPath
			view.HeroPath = game.HeroPath
			view.IconPath = game.IconPath
		}
		w.Header().Set("HX-Push-Url", "false")
		h.renderer.RenderPartial(w, "dashboard", "gameserver_card", view)
		return
	}

	// Otherwise return the detail page content
	h.renderer.Render(w, r, "gameservers/detail", map[string]any{
		"Gameserver": gs,
		"Game":       game,
	})
}
