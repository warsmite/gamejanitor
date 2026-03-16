package handlers

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"

	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/gorilla/csrf"
)

type PageDashboardHandlers struct {
	gameStore     *games.GameStore
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	settingsSvc   *service.SettingsService
	registry      *worker.Registry
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageDashboardHandlers(gameStore *games.GameStore, gameserverSvc *service.GameserverService, querySvc *service.QueryService, settingsSvc *service.SettingsService, registry *worker.Registry, renderer *Renderer, log *slog.Logger) *PageDashboardHandlers {
	return &PageDashboardHandlers{gameStore: gameStore, gameserverSvc: gameserverSvc, querySvc: querySvc, settingsSvc: settingsSvc, registry: registry, renderer: renderer, log: log}
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
	NodeID                     string
	NodeLabel                  string
	NodeDown                   bool
}

func shouldShowLogTail(status string) bool {
	switch status {
	case "starting", "started", "pulling", "error":
		return true
	}
	return false
}

func buildGameserverView(gs *models.Gameserver, game *games.Game, querySvc *service.QueryService, registry *worker.Registry, connectIP string, connectionConfigured bool) gameserverView {
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

	if registry != nil && gs.NodeID != nil && *gs.NodeID != "" {
		v.NodeID = *gs.NodeID
		if info, ok := registry.GetInfo(*gs.NodeID); ok {
			v.NodeLabel = info.LanIP
			v.NodeDown = time.Since(info.LastSeen) > 25*time.Second
		} else {
			v.NodeLabel = *gs.NodeID
			v.NodeDown = true
		}
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
	gameList := h.gameStore.ListGames()
	gameLookup := make(map[string]games.Game, len(gameList))
	for _, g := range gameList {
		gameLookup[g.ID] = g
	}

	var activeViews, stoppedViews []gameserverView
	gsCountByNode := make(map[string]int)
	for _, gs := range gameservers {
		game := gameLookup[gs.GameID]
		connectIP, connectionConfigured := h.settingsSvc.ResolveConnectionIP(gs.NodeID)
		if connectIP == "" {
			connectIP = "127.0.0.1"
		}
		v := buildGameserverView(&gs, &game, h.querySvc, h.registry, connectIP, connectionConfigured)
		if gs.Status == "stopped" {
			stoppedViews = append(stoppedViews, v)
		} else {
			activeViews = append(activeViews, v)
		}
		if gs.NodeID != nil && *gs.NodeID != "" {
			gsCountByNode[*gs.NodeID]++
		}
	}

	data := map[string]any{
		"ActiveGameservers":  activeViews,
		"StoppedGameservers": stoppedViews,
		"HasGameservers":     len(gameservers) > 0,
		"Games":              gameList,
		"RunningCount":       len(activeViews),
		"StoppedCount":       len(stoppedViews),
		"TotalCount":         len(gameservers),
	}

	if h.registry != nil {
		data["MultiNode"] = true
		data["Workers"] = h.buildDashboardWorkerViews(gsCountByNode)
	}

	h.renderer.Render(w, r, "dashboard", data)
}

type dashboardWorkerView struct {
	ID                string
	LanIP             string
	CPUCores          int64
	MemoryTotalMB     int64
	MemoryAvailableMB int64
	GameserverCount   int
	IsHealthy         bool
	IsWarning         bool
}

func (h *PageDashboardHandlers) buildDashboardWorkerViews(gsCountByNode map[string]int) []dashboardWorkerView {
	infos := h.registry.ListWorkers()
	views := make([]dashboardWorkerView, 0, len(infos))
	for _, info := range infos {
		age := time.Since(info.LastSeen)
		views = append(views, dashboardWorkerView{
			ID:                info.ID,
			LanIP:             info.LanIP,
			CPUCores:          info.CPUCores,
			MemoryTotalMB:     info.MemoryTotalMB,
			MemoryAvailableMB: info.MemoryAvailableMB,
			GameserverCount:   gsCountByNode[info.ID],
			IsHealthy:         age < 15*time.Second,
			IsWarning:         age >= 15*time.Second && age < 25*time.Second,
		})
	}
	return views
}

// WorkersPartial renders the dashboard worker summary for HTMX polling.
func (h *PageDashboardHandlers) WorkersPartial(w http.ResponseWriter, r *http.Request) {
	var views []dashboardWorkerView
	if h.registry != nil {
		gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
		if err != nil {
			h.log.Error("listing gameservers for worker partial", "error", err)
		}
		gsCountByNode := make(map[string]int)
		for _, gs := range gameservers {
			if gs.NodeID != nil && *gs.NodeID != "" {
				gsCountByNode[*gs.NodeID]++
			}
		}
		views = h.buildDashboardWorkerViews(gsCountByNode)
	}
	w.Header().Set("HX-Push-Url", "false")
	h.renderer.RenderPartial(w, "dashboard", "dashboard_workers", map[string]any{
		"Workers":   views,
		"CSRFToken": csrf.Token(r),
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
