package handlers

import (
	"bytes"
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
	settingsSvc   *service.SettingsService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageGameserverHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, querySvc *service.QueryService, settingsSvc *service.SettingsService, renderer *Renderer, log *slog.Logger) *PageGameserverHandlers {
	return &PageGameserverHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, querySvc: querySvc, settingsSvc: settingsSvc, renderer: renderer, log: log}
}

type gameserverFormData struct {
	MemoryLimitMB int
	CPULimit      float64
	Ports         json.RawMessage
	Env           json.RawMessage
	PortMode      string
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
	portMode := r.FormValue("port_mode")
	if portMode != "manual" {
		portMode = "auto"
	}

	return &gameserverFormData{
		MemoryLimitMB: memoryLimitMB,
		CPULimit:      cpuLimit,
		Ports:         validateJSONOrDefault(r.FormValue("ports_json"), "[]"),
		Env:           validateJSONOrDefault(r.FormValue("env_json"), "{}"),
		PortMode:      portMode,
	}, nil
}

// usedPortEntry tracks a host port claimed by another gameserver.
type usedPortEntry struct {
	Port           int    `json:"port"`
	GameserverName string `json:"gameserver_name"`
	GameserverID   string `json:"gameserver_id"`
}

// buildUsedPorts returns all host ports used by other gameservers (excludeID is omitted, e.g. the current gameserver in edit mode).
func (h *PageGameserverHandlers) buildUsedPorts(excludeID string) []usedPortEntry {
	allGS, err := h.gameserverSvc.ListGameservers(models.GameserverFilter{})
	if err != nil {
		h.log.Error("listing gameservers for port conflict check", "error", err)
		return nil
	}

	var used []usedPortEntry
	for _, gs := range allGS {
		if gs.ID == excludeID {
			continue
		}
		var ports []struct {
			HostPort json.Number `json:"host_port"`
			Port     json.Number `json:"port"`
		}
		dec := json.NewDecoder(bytes.NewReader(gs.Ports))
		dec.UseNumber()
		if err := dec.Decode(&ports); err != nil {
			continue
		}
		for _, p := range ports {
			hp, _ := p.HostPort.Int64()
			if hp == 0 {
				hp, _ = p.Port.Int64()
			}
			if hp != 0 {
				used = append(used, usedPortEntry{Port: int(hp), GameserverName: gs.Name, GameserverID: gs.ID})
			}
		}
	}
	return used
}

func (h *PageGameserverHandlers) New(w http.ResponseWriter, r *http.Request) {
	games, err := h.gameSvc.ListGames()
	if err != nil {
		h.log.Error("listing games for new gameserver form", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if games == nil {
		games = []models.Game{}
	}

	// Build JSON representation of games for Alpine.js
	type gameJSON struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		IconPath     string          `json:"icon_path"`
		GridPath     string          `json:"grid_path"`
		DefaultPorts json.RawMessage `json:"default_ports"`
		DefaultEnv   json.RawMessage `json:"default_env"`
		RecommendedMemoryMB int             `json:"recommended_memory_mb"`
	}
	gamesForJS := make([]gameJSON, len(games))
	for i, g := range games {
		gamesForJS[i] = gameJSON{
			ID:                  g.ID,
			Name:                g.Name,
			IconPath:            g.IconPath,
			GridPath:            g.GridPath,
			DefaultPorts:        g.DefaultPorts,
			DefaultEnv:          g.DefaultEnv,
			RecommendedMemoryMB: g.RecommendedMemoryMB,
		}
	}
	gamesJSONBytes, err := json.Marshal(gamesForJS)
	if err != nil {
		h.log.Error("marshaling games JSON", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}

	usedPortsJSON, _ := json.Marshal(h.buildUsedPorts(""))

	h.renderer.Render(w, r, "gameservers/form", map[string]any{
		"Mode":              "new",
		"Games":             games,
		"GamesJSON":         string(gamesJSONBytes),
		"PortsJSON":         "[]",
		"EnvJSON":           "{}",
		"UsedPortsJSON":     string(usedPortsJSON),
		"PreferredPortMode": h.settingsSvc.GetPreferredPortMode(),
		"CurrentPortMode":   "",
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
		PortMode:      form.PortMode,
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

// gameSetting is a human-readable game setting for display on the detail page.
type gameSetting struct {
	Label string
	Value string
	Type  string
}

// portEntry is a parsed port for display on the detail page.
type portEntry struct {
	Name          string
	HostPort      int
	ContainerPort int
	Protocol      string
}

// buildGameSettings merges game default env metadata with the gameserver's actual env values.
func buildGameSettings(game *models.Game, gs *models.Gameserver) []gameSetting {
	if game == nil {
		return nil
	}

	var defaults []struct {
		Key     string   `json:"key"`
		Label   string   `json:"label"`
		Type    string   `json:"type"`
		System  bool     `json:"system"`
		Options []string `json:"options"`
	}
	if err := json.Unmarshal(game.DefaultEnv, &defaults); err != nil {
		return nil
	}

	var envMap map[string]string
	if err := json.Unmarshal(gs.Env, &envMap); err != nil {
		return nil
	}

	var settings []gameSetting
	for _, d := range defaults {
		if d.System || d.Label == "" {
			continue
		}
		val := envMap[d.Key]
		if val == "" {
			continue
		}

		displayVal := val
		if d.Type == "boolean" {
			if val == "true" {
				displayVal = "Enabled"
			} else {
				displayVal = "Disabled"
			}
		}

		settings = append(settings, gameSetting{
			Label: d.Label,
			Value: displayVal,
			Type:  d.Type,
		})
	}
	return settings
}

// parsePorts extracts structured port entries from a gameserver's port JSON.
func parsePorts(portsJSON json.RawMessage) []portEntry {
	var raw []struct {
		Name          string `json:"name"`
		HostPort      int    `json:"host_port"`
		ContainerPort int    `json:"container_port"`
		Port          int    `json:"port"`
		Protocol      string `json:"protocol"`
	}
	if err := json.Unmarshal(portsJSON, &raw); err != nil {
		return nil
	}
	entries := make([]portEntry, len(raw))
	for i, p := range raw {
		hp := p.HostPort
		if hp == 0 {
			hp = p.Port
		}
		cp := p.ContainerPort
		if cp == 0 {
			cp = p.Port
		}
		entries[i] = portEntry{
			Name:          p.Name,
			HostPort:      hp,
			ContainerPort: cp,
			Protocol:      p.Protocol,
		}
	}
	return entries
}

func (h *PageGameserverHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil {
		h.log.Error("getting gameserver", "id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if gs == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
		return
	}

	game, err := h.gameSvc.GetGame(gs.GameID)
	if err != nil {
		h.log.Error("getting game for gameserver detail", "game_id", gs.GameID, "error", err)
	}
	h.renderer.Render(w, r, "gameservers/detail", map[string]any{
		"Gameserver":   gs,
		"Game":         game,
		"QueryData":    h.querySvc.GetQueryData(id),
		"GamePort":     firstGamePort(gs.Ports),
		"GameSettings": buildGameSettings(game, gs),
		"Ports":        parsePorts(gs.Ports),
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
		// Dashboard card delete uses hx-swap="delete" with a card target — just return 200
		// Detail page delete needs a redirect to dashboard
		if r.Header.Get("HX-Target") != "gs-"+id {
			w.Header().Set("HX-Redirect", "/")
		}
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
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if gs == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
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

	// Build single-game array for the shared form template
	gameForJS := struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		GridPath     string          `json:"grid_path"`
		DefaultPorts json.RawMessage `json:"default_ports"`
		DefaultEnv   json.RawMessage `json:"default_env"`
		RecommendedMemoryMB int             `json:"recommended_memory_mb"`
	}{}
	if game != nil {
		gameForJS.ID = game.ID
		gameForJS.Name = game.Name
		gameForJS.GridPath = game.GridPath
		gameForJS.DefaultPorts = game.DefaultPorts
		gameForJS.DefaultEnv = game.DefaultEnv
		gameForJS.RecommendedMemoryMB = game.RecommendedMemoryMB
	}
	gamesJSONBytes, _ := json.Marshal([]any{gameForJS})

	usedPortsJSON, _ := json.Marshal(h.buildUsedPorts(gs.ID))

	h.renderer.Render(w, r, "gameservers/form", map[string]any{
		"Mode":              "edit",
		"Gameserver":        gs,
		"Game":              game,
		"Games":             []models.Game{*game},
		"GamesJSON":         string(gamesJSONBytes),
		"PortsJSON":         portsJSON,
		"EnvJSON":           envJSON,
		"UsedPortsJSON":     string(usedPortsJSON),
		"PreferredPortMode": h.settingsSvc.GetPreferredPortMode(),
		"CurrentPortMode":   gs.PortMode,
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

	// If switching to auto, re-allocate ports
	ports := form.Ports
	if form.PortMode == "auto" {
		game, err := h.gameSvc.GetGame(existing.GameID)
		if err != nil || game == nil {
			h.log.Error("getting game for port allocation", "game_id", existing.GameID, "error", err)
			http.Error(w, "Failed to load game for port allocation", http.StatusInternalServerError)
			return
		}
		allocatedPorts, err := h.gameserverSvc.AllocatePorts(game, existing.ID)
		if err != nil {
			h.log.Error("auto-allocating ports during update", "id", id, "error", err)
			http.Error(w, "Failed to allocate ports: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ports = allocatedPorts
	}

	// Preserve immutable fields from existing record
	gs := &models.Gameserver{
		ID:            existing.ID,
		Name:          name,
		GameID:        existing.GameID,
		Ports:         ports,
		Env:           form.Env,
		MemoryLimitMB: form.MemoryLimitMB,
		CPULimit:      form.CPULimit,
		ContainerID:   existing.ContainerID,
		VolumeName:    existing.VolumeName,
		Status:        existing.Status,
		PortMode:      form.PortMode,
		CreatedAt:     existing.CreatedAt,
	}

	if err := h.gameserverSvc.UpdateGameserver(gs); err != nil {
		h.log.Error("updating gameserver from web form", "id", id, "error", err)
		http.Error(w, "Failed to update gameserver: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if r.FormValue("restart") == "true" {
		if err := h.gameserverSvc.Restart(r.Context(), id); err != nil {
			h.log.Error("restarting gameserver after update", "id", id, "error", err)
		}
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
	connectIP := h.settingsSvc.GetConnectionAddress()
	connectionConfigured := connectIP != ""
	if connectIP == "" {
		connectIP = "127.0.0.1"
	}
	view := buildGameserverView(gs, game, h.querySvc, connectIP, connectionConfigured)

	w.Header().Set("HX-Push-Url", "false")
	h.renderer.RenderPartial(w, "dashboard", "gameserver_card", view)
}
