package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

// validGameID is shared with API handlers for consistent validation.
var validGameID = regexp.MustCompile(`^[a-z0-9\-]+$`)

type PageGameHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageGameHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageGameHandlers {
	return &PageGameHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageGameHandlers) List(w http.ResponseWriter, r *http.Request) {
	games, err := h.gameSvc.ListGames()
	if err != nil {
		h.log.Error("listing games", "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if games == nil {
		games = []models.Game{}
	}

	h.renderer.Render(w, r, "games/list", map[string]any{
		"Games": games,
	})
}

func (h *PageGameHandlers) Detail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	game, err := h.gameSvc.GetGame(id)
	if err != nil {
		h.log.Error("getting game", "id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
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

func (h *PageGameHandlers) New(w http.ResponseWriter, r *http.Request) {
	h.renderer.Render(w, r, "games/new", map[string]any{})
}

func (h *PageGameHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	id := r.FormValue("id")
	name := r.FormValue("name")
	image := r.FormValue("image")
	if id == "" || name == "" || image == "" {
		http.Error(w, "ID, name, and image are required", http.StatusBadRequest)
		return
	}
	if !validGameID.MatchString(id) {
		http.Error(w, "Game ID must contain only lowercase letters, numbers, and hyphens", http.StatusBadRequest)
		return
	}

	game, err := h.parseGameForm(r, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := h.gameSvc.CreateGame(game); err != nil {
		h.log.Error("creating game from web form", "id", id, "error", err)
		http.Error(w, "Failed to create game: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+id)
	http.Redirect(w, r, "/games/"+id, http.StatusSeeOther)
}

func (h *PageGameHandlers) Edit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	game, err := h.gameSvc.GetGame(id)
	if err != nil {
		h.log.Error("getting game for edit", "id", id, "error", err)
		h.renderer.RenderError(w, r, http.StatusInternalServerError)
		return
	}
	if game == nil {
		h.renderer.RenderError(w, r, http.StatusNotFound)
		return
	}

	defaultPortsJSON := "[]"
	if len(game.DefaultPorts) > 0 {
		defaultPortsJSON = string(game.DefaultPorts)
	}
	defaultEnvJSON := "[]"
	if len(game.DefaultEnv) > 0 {
		defaultEnvJSON = string(game.DefaultEnv)
	}
	disabledCapsJSON := "[]"
	if len(game.DisabledCapabilities) > 0 {
		disabledCapsJSON = string(game.DisabledCapabilities)
	}

	h.renderer.Render(w, r, "games/edit", map[string]any{
		"Game":             game,
		"DefaultPortsJSON": defaultPortsJSON,
		"DefaultEnvJSON":   defaultEnvJSON,
		"DisabledCapsJSON": disabledCapsJSON,
	})
}

func (h *PageGameHandlers) Update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	existing, err := h.gameSvc.GetGame(id)
	if err != nil {
		h.log.Error("getting game for update", "id", id, "error", err)
		http.Error(w, "Failed to load game", http.StatusInternalServerError)
		return
	}
	if existing == nil {
		http.Error(w, "Game not found", http.StatusNotFound)
		return
	}

	game, err := h.parseGameForm(r, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	game.CreatedAt = existing.CreatedAt

	if err := h.gameSvc.UpdateGame(game); err != nil {
		h.log.Error("updating game from web form", "id", id, "error", err)
		http.Error(w, "Failed to update game: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games/"+id)
	http.Redirect(w, r, "/games/"+id, http.StatusSeeOther)
}

func (h *PageGameHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.gameSvc.DeleteGame(id); err != nil {
		h.log.Error("deleting game from web", "id", id, "error", err)
		http.Error(w, "Failed to delete game: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("HX-Redirect", "/games")
	http.Redirect(w, r, "/games", http.StatusSeeOther)
}

func (h *PageGameHandlers) parseGameForm(r *http.Request, id string) (*models.Game, error) {
	recommendedMemoryMB, err := strconv.Atoi(r.FormValue("recommended_memory_mb"))
	if err != nil && r.FormValue("recommended_memory_mb") != "" {
		return nil, fmt.Errorf("invalid recommended_memory_mb: %w", err)
	}

	defaultPorts := validateJSONOrDefault(r.FormValue("default_ports_json"), "[]")
	defaultEnv := validateJSONOrDefault(r.FormValue("default_env_json"), "[]")
	disabledCaps := validateJSONOrDefault(r.FormValue("disabled_capabilities_json"), "[]")

	var gsqSlug *string
	if v := r.FormValue("gsq_game_slug"); v != "" {
		gsqSlug = &v
	}

	return &models.Game{
		ID:                   id,
		Name:                 r.FormValue("name"),
		Image:                r.FormValue("image"),
		IconPath:             r.FormValue("icon_path"),
		GridPath:             r.FormValue("grid_path"),
		HeroPath:             r.FormValue("hero_path"),
		DefaultPorts:         defaultPorts,
		DefaultEnv:           defaultEnv,
		RecommendedMemoryMB:  recommendedMemoryMB,
		GSQGameSlug:          gsqSlug,
		DisabledCapabilities: disabledCaps,
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
