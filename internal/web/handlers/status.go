package handlers

import (
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
)

type StatusHandlers struct {
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	docker        *docker.Client
	log           *slog.Logger
}

func NewStatusHandlers(gameserverSvc *service.GameserverService, querySvc *service.QueryService, dockerClient *docker.Client, log *slog.Logger) *StatusHandlers {
	return &StatusHandlers{gameserverSvc: gameserverSvc, querySvc: querySvc, docker: dockerClient, log: log}
}

type gameserverOverview struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	GameID        string  `json:"game_id"`
	Status        string  `json:"status"`
	MemoryUsageMB int     `json:"memory_usage_mb"`
	MemoryLimitMB int     `json:"memory_limit_mb"`
	CPUPercent    float64 `json:"cpu_percent"`
	PlayersOnline *int    `json:"players_online"`
	MaxPlayers    *int    `json:"max_players"`
}

type statusSummary struct {
	Total   int `json:"total"`
	Running int `json:"running"`
	Stopped int `json:"stopped"`
}

func (h *StatusHandlers) Get(w http.ResponseWriter, r *http.Request) {
	gameservers, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for status", "error", err)
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	summary := statusSummary{Total: len(gameservers)}
	overviews := make([]gameserverOverview, 0, len(gameservers))

	for _, gs := range gameservers {
		o := gameserverOverview{
			ID:            gs.ID,
			Name:          gs.Name,
			GameID:        gs.GameID,
			Status:        gs.Status,
			MemoryLimitMB: gs.MemoryLimitMB,
		}

		isRunning := gs.Status == "started" || gs.Status == "running"

		if isRunning {
			summary.Running++

			if gs.ContainerID != nil {
				stats, err := h.docker.ContainerStats(r.Context(), *gs.ContainerID)
				if err != nil {
					h.log.Warn("failed to get container stats", "id", gs.ID, "error", err)
				} else {
					o.MemoryUsageMB = stats.MemoryUsageMB
					o.CPUPercent = stats.CPUPercent
				}
			}

			if qd := h.querySvc.GetQueryData(gs.ID); qd != nil {
				o.PlayersOnline = &qd.PlayersOnline
				o.MaxPlayers = &qd.MaxPlayers
			}
		} else if gs.Status == "stopped" {
			summary.Stopped++
		}

		overviews = append(overviews, o)
	}

	respondOK(w, map[string]any{
		"gameservers": overviews,
		"summary":     summary,
	})
}
