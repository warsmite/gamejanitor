package handlers

import (
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type PageDashboardHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageDashboardHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, querySvc *service.QueryService, renderer *Renderer, log *slog.Logger) *PageDashboardHandlers {
	return &PageDashboardHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, querySvc: querySvc, renderer: renderer, log: log}
}

type gameserverView struct {
	ID            string
	Name          string
	GameID        string
	GameName      string
	GridPath      string
	HeroPath      string
	Status        string
	PlayersOnline int
	MaxPlayers    int
	HasQueryData  bool
}

func (h *PageDashboardHandlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for dashboard", "error", err)
		http.Error(w, "Failed to load dashboard", http.StatusInternalServerError)
		return
	}

	// Build game lookup
	games, err := h.gameSvc.ListGames()
	if err != nil {
		h.log.Error("listing games for dashboard", "error", err)
		http.Error(w, "Failed to load dashboard", http.StatusInternalServerError)
		return
	}
	gameLookup := make(map[string]models.Game, len(games))
	for _, g := range games {
		gameLookup[g.ID] = g
	}

	var activeViews, stoppedViews []gameserverView
	for _, gs := range gameservers {
		game := gameLookup[gs.GameID]
		v := gameserverView{
			ID:       gs.ID,
			Name:     gs.Name,
			GameID:   gs.GameID,
			GameName: game.Name,
			GridPath: game.GridPath,
			HeroPath: game.HeroPath,
			Status:   gs.Status,
		}
		if qd := h.querySvc.GetQueryData(gs.ID); qd != nil {
			v.PlayersOnline = qd.PlayersOnline
			v.MaxPlayers = qd.MaxPlayers
			v.HasQueryData = true
		}
		if gs.Status == "stopped" {
			stoppedViews = append(stoppedViews, v)
		} else {
			activeViews = append(activeViews, v)
		}
	}

	h.renderer.Render(w, r, "dashboard", map[string]any{
		"ActiveGameservers":  activeViews,
		"StoppedGameservers": stoppedViews,
		"HasGameservers":     len(gameservers) > 0,
		"Games":              games,
	})
}
