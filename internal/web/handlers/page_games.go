package handlers

import (
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageGameHandlers struct {
	gameStore     *games.GameStore
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageGameHandlers(gameStore *games.GameStore, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageGameHandlers {
	return &PageGameHandlers{gameStore: gameStore, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageGameHandlers) List(w http.ResponseWriter, r *http.Request) {
	gameList := h.gameStore.ListGames()
	if gameList == nil {
		gameList = []games.Game{}
	}

	h.renderer.Render(w, r, "games/list", map[string]any{
		"Games": gameList,
	})
}

func (h *PageGameHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	game := h.gameStore.GetGame(id)
	if game == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
		return
	}

	// Find gameservers using this game
	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{GameID: &id})
	if err != nil {
		h.log.Error("listing gameservers for game", "game_id", id, "error", err)
		gameservers = []models.Gameserver{}
	}

	h.renderer.Render(w, r, "games/detail", map[string]any{
		"Game":        game,
		"Gameservers": gameservers,
	})
}
