package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/warsmite/gamejanitor/internal/games"
	"github.com/warsmite/gamejanitor/internal/models"
	"github.com/warsmite/gamejanitor/internal/service"
	"github.com/warsmite/gamejanitor/internal/worker"
	"github.com/go-chi/chi/v5"
)

type PageGameserverHandlers struct {
	gameStore     *games.GameStore
	gameserverSvc *service.GameserverService
	scheduleSvc   *service.ScheduleService
	querySvc      *service.QueryService
	settingsSvc   *service.SettingsService
	registry      *worker.Registry
	renderer      *Renderer
	db            *sql.DB
	log           *slog.Logger
}

func NewPageGameserverHandlers(gameStore *games.GameStore, gameserverSvc *service.GameserverService, scheduleSvc *service.ScheduleService, querySvc *service.QueryService, settingsSvc *service.SettingsService, registry *worker.Registry, renderer *Renderer, db *sql.DB, log *slog.Logger) *PageGameserverHandlers {
	return &PageGameserverHandlers{gameStore: gameStore, gameserverSvc: gameserverSvc, scheduleSvc: scheduleSvc, querySvc: querySvc, settingsSvc: settingsSvc, registry: registry, renderer: renderer, db: db, log: log}
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

func validateJSONOrDefault(raw string, fallback string) json.RawMessage {
	if raw == "" {
		return json.RawMessage(fallback)
	}
	if !json.Valid([]byte(raw)) {
		return json.RawMessage(fallback)
	}
	return json.RawMessage(raw)
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
			HostPort int `json:"host_port"`
		}
		if err := json.Unmarshal(gs.Ports, &ports); err != nil {
			continue
		}
		for _, p := range ports {
			if p.HostPort != 0 {
				used = append(used, usedPortEntry{Port: p.HostPort, GameserverName: gs.Name, GameserverID: gs.ID})
			}
		}
	}
	return used
}

func (h *PageGameserverHandlers) New(w http.ResponseWriter, r *http.Request) {
	gameList := h.gameStore.ListGames()
	if gameList == nil {
		gameList = []games.Game{}
	}

	// Build JSON representation of games for Alpine.js
	type gameJSON struct {
		ID           string          `json:"id"`
		Name         string          `json:"name"`
		IconPath     string          `json:"icon_path"`
		GridPath     string          `json:"grid_path"`
		DefaultPorts []games.Port   `json:"default_ports"`
		DefaultEnv   []games.EnvVar `json:"default_env"`
		RecommendedMemoryMB int            `json:"recommended_memory_mb"`
	}
	gamesForJS := make([]gameJSON, len(gameList))
	for i, g := range gameList {
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

	usedPorts := h.buildUsedPorts("")
	if usedPorts == nil {
		usedPorts = []usedPortEntry{}
	}
	usedPortsJSON, _ := json.Marshal(usedPorts)

	data := map[string]any{
		"Mode":              "new",
		"Games":             gameList,
		"GamesJSON":         string(gamesJSONBytes),
		"PortsJSON":         "[]",
		"EnvJSON":           "{}",
		"UsedPortsJSON":     string(usedPortsJSON),
		"PreferredPortMode": h.settingsSvc.GetPreferredPortMode(),
		"CurrentPortMode":   "",
		"IsAdmin":           true,
		"PortRangeStart":    h.settingsSvc.GetPortRangeStart(),
		"PortRangeEnd":      h.settingsSvc.GetPortRangeEnd(),
	}

	if h.registry != nil {
		type workerOption struct {
			ID                string `json:"id"`
			LanIP             string `json:"lan_ip"`
			CPUCores          int64  `json:"cpu_cores"`
			MemoryTotalMB     int64  `json:"memory_total_mb"`
			MemoryAvailableMB int64  `json:"memory_available_mb"`
		}
		infos := h.registry.ListWorkers()
		opts := make([]workerOption, 0, len(infos))
		for _, info := range infos {
			opts = append(opts, workerOption{
				ID:                info.ID,
				LanIP:             info.LanIP,
				CPUCores:          info.CPUCores,
				MemoryTotalMB:     info.MemoryTotalMB,
				MemoryAvailableMB: info.MemoryAvailableMB,
			})
		}
		workersJSON, _ := json.Marshal(opts)
		data["MultiNode"] = true
		data["WorkersJSON"] = string(workersJSON)
	}

	h.renderer.Render(w, r, "gameservers/form", data)
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
		AutoRestart:   true,
	}

	if nodeID := r.FormValue("node_id"); nodeID != "" {
		gs.NodeID = &nodeID
	}

	rawPassword, err := h.gameserverSvc.CreateGameserver(r.Context(), gs)
	if err != nil {
		h.log.Error("creating gameserver from web form", "error", err)
		http.Error(w, "Failed to create gameserver: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Create a default daily backup schedule for new gameservers
	defaultBackup := &models.Schedule{
		GameserverID: gs.ID,
		Name:         "Daily Backup",
		Type:         "backup",
		CronExpr:     "0 4 * * *",
		Payload:      json.RawMessage(`{}`),
		Enabled:      true,
	}
	if err := h.scheduleSvc.CreateSchedule(defaultBackup); err != nil {
		h.log.Error("failed to create default backup schedule", "gameserver_id", gs.ID, "error", err)
	}

	h.renderer.Render(w, r, "gameservers/sftp_password", map[string]any{
		"GameserverID":   gs.ID,
		"GameserverName": gs.Name,
		"SFTPUsername":   gs.SFTPUsername,
		"SFTPPassword":   rawPassword,
		"IsCreate":       true,
	})
}

func (h *PageGameserverHandlers) RegenerateSFTPPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rawPassword, err := h.gameserverSvc.RegenerateSFTPPassword(r.Context(), id)
	if err != nil {
		h.log.Error("regenerating sftp password from web", "id", id, "error", err)
		http.Error(w, "Failed to regenerate SFTP password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	gs, err := h.gameserverSvc.GetGameserver(id)
	if err != nil || gs == nil {
		http.Error(w, "Gameserver not found", http.StatusNotFound)
		return
	}

	h.renderer.Render(w, r, "gameservers/sftp_password", map[string]any{
		"GameserverID":   id,
		"GameserverName": gs.Name,
		"SFTPUsername":   gs.SFTPUsername,
		"SFTPPassword":   rawPassword,
	})
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
func buildGameSettings(game *games.Game, gs *models.Gameserver) []gameSetting {
	if game == nil {
		return nil
	}

	var envMap map[string]string
	if err := json.Unmarshal(gs.Env, &envMap); err != nil {
		return nil
	}

	var settings []gameSetting
	for _, d := range game.DefaultEnv {
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
		Protocol      string `json:"protocol"`
	}
	if err := json.Unmarshal(portsJSON, &raw); err != nil {
		return nil
	}
	entries := make([]portEntry, len(raw))
	for i, p := range raw {
		entries[i] = portEntry{
			Name:          p.Name,
			HostPort:      p.HostPort,
			ContainerPort: p.ContainerPort,
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

	game := h.gameStore.GetGame(gs.GameID)
	connectIP, connectionConfigured := h.settingsSvc.ResolveConnectionIP(gs.NodeID)
	if connectIP == "" {
		connectIP = "127.0.0.1"
	}

	sftpPort := 0
	if gs.NodeID != nil && *gs.NodeID != "" {
		if node, err := models.GetWorkerNode(h.db, *gs.NodeID); err == nil && node != nil && node.SFTPPort > 0 {
			sftpPort = node.SFTPPort
		}
	}

	h.renderer.Render(w, r, "gameservers/detail", map[string]any{
		"Gameserver":                 gs,
		"Game":                       game,
		"QueryData":                  h.querySvc.GetQueryData(id),
		"GamePort":                   firstGamePort(gs.Ports),
		"GameSettings":               buildGameSettings(game, gs),
		"Ports":                      parsePorts(gs.Ports),
		"ConnectionAddress":          connectIP,
		"ConnectionAddressConfigured": connectionConfigured,
		"SFTPPort":                   sftpPort,
	})
}

func (h *PageGameserverHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Use background context — delete must complete even if the HTTP request is cancelled.
	// Preserve token for actor attribution.
	ctx := context.Background()
	if token := service.TokenFromContext(r.Context()); token != nil {
		ctx = service.SetTokenInContext(ctx, token)
	}
	if err := h.gameserverSvc.DeleteGameserver(ctx, id); err != nil {
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

	game := h.gameStore.GetGame(gs.GameID)

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
		DefaultPorts []games.Port   `json:"default_ports"`
		DefaultEnv   []games.EnvVar `json:"default_env"`
		RecommendedMemoryMB int            `json:"recommended_memory_mb"`
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

	editUsedPorts := h.buildUsedPorts(gs.ID)
	if editUsedPorts == nil {
		editUsedPorts = []usedPortEntry{}
	}
	usedPortsJSON, _ := json.Marshal(editUsedPorts)

	gamesForTemplate := []games.Game{}
	if game != nil {
		gamesForTemplate = []games.Game{*game}
	}

	token := service.TokenFromContext(r.Context())
	isAdmin := token == nil || service.IsAdmin(token)

	h.renderer.Render(w, r, "gameservers/form", map[string]any{
		"Mode":              "edit",
		"Gameserver":        gs,
		"Game":              game,
		"Games":             gamesForTemplate,
		"GamesJSON":         string(gamesJSONBytes),
		"PortsJSON":         portsJSON,
		"EnvJSON":           envJSON,
		"UsedPortsJSON":     string(usedPortsJSON),
		"PreferredPortMode": h.settingsSvc.GetPreferredPortMode(),
		"CurrentPortMode":   gs.PortMode,
		"PortRangeStart":    h.settingsSvc.GetPortRangeStart(),
		"PortRangeEnd":      h.settingsSvc.GetPortRangeEnd(),
		"IsAdmin":           isAdmin,
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
		game := h.gameStore.GetGame(existing.GameID)
		if game == nil {
			h.log.Error("getting game for port allocation", "game_id", existing.GameID)
			http.Error(w, "Failed to load game for port allocation", http.StatusInternalServerError)
			return
		}
		nodeID := ""
		if existing.NodeID != nil {
			nodeID = *existing.NodeID
		}
		allocatedPorts, err := h.gameserverSvc.AllocatePorts(game, nodeID, existing.ID)
		if err != nil {
			h.log.Error("auto-allocating ports during update", "id", id, "error", err)
			http.Error(w, "Failed to allocate ports: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ports = allocatedPorts
	}

	var backupLimit *int
	if v := r.FormValue("backup_limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			backupLimit = &n
		}
	}
	var storageLimitMB *int
	if v := r.FormValue("storage_limit_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			storageLimitMB = &n
		}
	}

	gs := &models.Gameserver{
		ID:             existing.ID,
		Name:           name,
		GameID:         existing.GameID,
		Ports:          ports,
		Env:            form.Env,
		MemoryLimitMB:  form.MemoryLimitMB,
		CPULimit:       form.CPULimit,
		CPUEnforced:    r.FormValue("cpu_enforced") == "on",
		ContainerID:    existing.ContainerID,
		VolumeName:     existing.VolumeName,
		Status:         existing.Status,
		PortMode:       form.PortMode,
		BackupLimit:    backupLimit,
		StorageLimitMB: storageLimitMB,
		AutoRestart:    r.FormValue("auto_restart") == "on",
		CreatedAt:      existing.CreatedAt,
	}

	if _, err := h.gameserverSvc.UpdateGameserver(r.Context(), gs); err != nil {
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

	game := h.gameStore.GetGame(gs.GameID)
	connectIP, connectionConfigured := h.settingsSvc.ResolveConnectionIP(gs.NodeID)
	if connectIP == "" {
		connectIP = "127.0.0.1"
	}
	view := buildGameserverView(gs, game, h.querySvc, h.registry, connectIP, connectionConfigured)

	w.Header().Set("HX-Push-Url", "false")
	h.renderer.RenderPartial(w, "dashboard", "gameserver_card", view)
}
