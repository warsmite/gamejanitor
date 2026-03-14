package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type PageDashboardHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	settingsSvc   *service.SettingsService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageDashboardHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, querySvc *service.QueryService, settingsSvc *service.SettingsService, renderer *Renderer, log *slog.Logger) *PageDashboardHandlers {
	return &PageDashboardHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, querySvc: querySvc, settingsSvc: settingsSvc, renderer: renderer, log: log}
}

type gameserverView struct {
	ID                         string
	Name                       string
	GameID                     string
	GameName                   string
	GridPath                   string
	HeroPath                   string
	IconPath                   string
	GamePort                   string
	ConnectAddress             string
	ConnectionAddressConfigured bool
	Status                     string
	PlayersOnline              int
	MaxPlayers                 int
	HasQueryData               bool
	ShowLogTail                bool
	ErrorReason                string
}

func shouldShowLogTail(status string) bool {
	switch status {
	case "starting", "started", "pulling", "error":
		return true
	}
	return false
}

func buildGameserverView(gs *models.Gameserver, game *models.Game, querySvc *service.QueryService, connectIP string, connectionConfigured bool) gameserverView {
	port := firstGamePort(gs.Ports)
	connectAddr := ""
	if port != "" && connectIP != "" {
		connectAddr = connectIP + ":" + port
	}

	v := gameserverView{
		ID:                          gs.ID,
		Name:                        gs.Name,
		GameID:                      gs.GameID,
		Status:                      gs.Status,
		ErrorReason:                 gs.ErrorReason,
		GamePort:                    port,
		ConnectAddress:              connectAddr,
		ConnectionAddressConfigured: connectionConfigured,
		ShowLogTail:                 shouldShowLogTail(gs.Status),
	}
	if game != nil {
		v.GameName = game.Name
		v.GridPath = game.GridPath
		v.HeroPath = game.HeroPath
		v.IconPath = game.IconPath
	}
	if qd := querySvc.GetQueryData(gs.ID); qd != nil {
		v.PlayersOnline = qd.PlayersOnline
		v.MaxPlayers = qd.MaxPlayers
		v.HasQueryData = true
	}
	return v
}

func (h *PageDashboardHandlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for dashboard", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}

	// Build game lookup
	games, err := h.gameSvc.ListGames()
	if err != nil {
		h.log.Error("listing games for dashboard", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	gameLookup := make(map[string]models.Game, len(games))
	for _, g := range games {
		gameLookup[g.ID] = g
	}

	connectIP := h.settingsSvc.GetConnectionAddress()
	connectionConfigured := connectIP != ""
	if connectIP == "" {
		connectIP = "127.0.0.1"
	}

	var activeViews, stoppedViews []gameserverView
	for _, gs := range gameservers {
		game := gameLookup[gs.GameID]
		v := buildGameserverView(&gs, &game, h.querySvc, connectIP, connectionConfigured)
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
		"RunningCount":       len(activeViews),
		"StoppedCount":       len(stoppedViews),
		"TotalCount":         len(gameservers),
	})
}

// firstGamePort extracts the first game port number from a gameserver's port config.
// Uses json.Number to handle host_port stored as either int or string.
func firstGamePort(portsJSON json.RawMessage) string {
	if len(portsJSON) == 0 {
		return ""
	}
	var ports []struct {
		HostPort json.Number `json:"host_port"`
		Name     string      `json:"name"`
	}
	dec := json.NewDecoder(bytes.NewReader(portsJSON))
	dec.UseNumber()
	if err := dec.Decode(&ports); err != nil || len(ports) == 0 {
		return ""
	}
	for _, p := range ports {
		if p.Name == "game" {
			return p.HostPort.String()
		}
	}
	return ports[0].HostPort.String()
}
