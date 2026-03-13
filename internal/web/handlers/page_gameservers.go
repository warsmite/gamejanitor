package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageGameserverHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	querySvc      *service.QueryService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageGameserverHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, querySvc *service.QueryService, renderer *Renderer, log *slog.Logger) *PageGameserverHandlers {
	return &PageGameserverHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, querySvc: querySvc, renderer: renderer, log: log}
}

type gameserverFormData struct {
	MemoryLimitMB int
	CPULimit      float64
	Ports         json.RawMessage
	Env           json.RawMessage
}

func parseGameserverForm(r *http.Request) (*gameserverFormData, error) {
	memoryLimitMB, err := strconv.Atoi(r.FormValue("memory_limit_mb"))
	if err != nil && r.FormValue("memory_limit_mb") != "" {
		return nil, fmt.Errorf("invalid memory value")
	}
	cpuLimit, err := strconv.ParseFloat(r.FormValue("cpu_limit"), 64)
	if err != nil && r.FormValue("cpu_limit") != "" {
		return nil, fmt.Errorf("invalid CPU value")
	}
	return &gameserverFormData{
		MemoryLimitMB: memoryLimitMB,
		CPULimit:      cpuLimit,
		Ports:         validateJSONOrDefault(r.FormValue("ports_json"), "[]"),
		Env:           validateJSONOrDefault(r.FormValue("env_json"), "{}"),
	}, nil
}

func (h *PageGameserverHandlers) New(w http.ResponseWriter, r *http.Request) {
	games, err := h.gameSvc.ListGames()
	if err != nil {
		h.log.Error("listing games for new gameserver form", "error", err)
		http.Error(w, "Failed to load form", http.StatusInternalServerError)
		return
	}
	if games == nil {
		games = []models.Game{}
	}

	// Build JSON representation of games for Alpine.js
	type gameJSON struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		GridPath     string          `json:"grid_path"`
		DefaultPorts json.RawMessage `json:"default_ports"`
		DefaultEnv   json.RawMessage `json:"default_env"`
		MinMemoryMB  int             `json:"min_memory_mb"`
		MinCPU       float64         `json:"min_cpu"`
	}
	gamesForJS := make([]gameJSON, len(games))
	for i, g := range games {
		gamesForJS[i] = gameJSON{
			ID:           g.ID,
			Name:         g.Name,
			GridPath:     g.GridPath,
			DefaultPorts: g.DefaultPorts,
			DefaultEnv:   g.DefaultEnv,
			MinMemoryMB:  g.MinMemoryMB,
			MinCPU:       g.MinCPU,
		}
	}
	gamesJSONBytes, err := json.Marshal(gamesForJS)
	if err != nil {
		h.log.Error("marshaling games JSON", "error", err)
		http.Error(w, "Failed to prepare form data", http.StatusInternalServerError)
		return
	}

	h.renderer.Render(w, r, "gameservers/new", map[string]any{
		"Games":     games,
		"GamesJSON": string(gamesJSONBytes),
	})
}

func (h *PageGameserverHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	gameID := r.FormValue("game_id")
	if name == "" || gameID == "" {
		http.Error(w, "Name and game are required", http.StatusBadRequest)
		return
	}

	form, err := parseGameserverForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	gs := &models.Gameserver{
		Name:          name,
		GameID:        gameID,
		Ports:         form.Ports,
		Env:           form.Env,
		MemoryLimitMB: form.MemoryLimitMB,
		CPULimit:      form.CPULimit,
	}

	if err := h.gameserverSvc.CreateGameserver(r.Context(), gs); err != nil {
		h.log.Error("creating gameserver from web form", "error", err)
		http.Error(w, "Failed to create gameserver: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect to detail page
	w.Header().Set("HX-Redirect", "/gameservers/"+gs.ID)
	http.Redirect(w, r, "/gameservers/"+gs.ID, http.StatusSeeOther)
}

func (h *PageGameserverHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for gameserver detail", "game_id", gs.GameID, "error", err)
	}
	h.renderer.Render(w, r, "gameservers/detail", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"QueryData":  h.querySvc.GetQueryData(id),
	})
}

func (h *PageGameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.gameserverSvc.DeleteGameserver(r.Context(), id); err != nil {
		h.log.Error("deleting gameserver from web", "id", id, "error", err)
		http.Error(w, "Failed to delete gameserver: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusOK)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *PageGameserverHandlers) Edit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for edit", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for gameserver edit", "game_id", gs.GameID, "error", err)
	}

	portsJSON := "[]"
	if len(gs.Ports) > 0 {
		portsJSON = string(gs.Ports)
	}
	envJSON := "{}"
	if len(gs.Env) > 0 {
		envJSON = string(gs.Env)
	}

	h.renderer.Render(w, r, "gameservers/edit", map[string]any{
		"Gameserver": gs,
		"Game":       game,
		"PortsJSON":  portsJSON,
		"EnvJSON":    envJSON,
	})
}

func (h *PageGameserverHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	existing, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for update", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "Name is required", http.StatusBadRequest)
		return
	}

	form, err := parseGameserverForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Preserve immutable fields from existing record
	gs := &models.Gameserver{
		ID:            existing.ID,
		Name:          name,
		GameID:        existing.GameID,
		Ports:         form.Ports,
		Env:           form.Env,
		MemoryLimitMB: form.MemoryLimitMB,
		CPULimit:      form.CPULimit,
		ContainerID:   existing.ContainerID,
		VolumeName:    existing.VolumeName,
		Status:        existing.Status,
		CreatedAt:     existing.CreatedAt,
	}

	if err := h.gameserverSvc.UpdateGameserver(gs); err != nil {
		h.log.Error("updating gameserver from web form", "id", id, "error", err)
		http.Error(w, "Failed to update gameserver: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/gameservers/"+id)
	http.Redirect(w, r, "/gameservers/"+id, http.StatusSeeOther)
}

// Card renders just the gameserver card partial (for SSE-triggered refreshes).
func (h *PageGameserverHandlers) Card(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver for card", "id", id, "error", err)
		http.Error(w, "Failed to load gameserver", http.StatusInternalServerError)
		return
	}
	if gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for card", "game_id", gs.GameID, "error", err)
	}
	view := buildGameserverView(gs, game, h.querySvc)

	w.Header().Set("HX-Push-Url", "false")
	h.renderer.RenderPartial(w, "dashboard", "gameserver_card", view)
}
