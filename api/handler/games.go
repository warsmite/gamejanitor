package handler

import (
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/games"
	"github.com/go-chi/chi/v5"
)

type GameHandlers struct {
	store    *games.GameStore
	options  *games.OptionsRegistry
	log      *slog.Logger
}

func NewGameHandlers(store *games.GameStore, options *games.OptionsRegistry, log *slog.Logger) *GameHandlers {
	return &GameHandlers{store: store, options: options, log: log}
}

func (h *GameHandlers) List(w http.ResponseWriter, r *http.Request) {
	gameList := h.store.ListGames()
	if gameList == nil {
		gameList = []games.Game{}
	}
	respondOK(w, gameList)
}

func (h *GameHandlers) Get(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	game := h.store.GetGame(id)
	if game == nil {
		respondError(w, http.StatusNotFound, "game "+id+" not found")
		return
	}
	respondOK(w, game)
}

// Options returns the dynamic options for a game's env var.
// GET /api/games/{id}/options/{key}
func (h *GameHandlers) Options(w http.ResponseWriter, r *http.Request) {
	gameID := chi.URLParam(r, "id")
	key := chi.URLParam(r, "key")

	game := h.store.GetGame(gameID)
	if game == nil {
		respondError(w, http.StatusNotFound, "game "+gameID+" not found")
		return
	}

	// Find the env var with this key
	var envVar *games.EnvVar
	for i := range game.DefaultEnv {
		if game.DefaultEnv[i].Key == key {
			envVar = &game.DefaultEnv[i]
			break
		}
	}
	if envVar == nil {
		respondError(w, http.StatusNotFound, "env var "+key+" not found for game "+gameID)
		return
	}
	if envVar.DynamicOptions == nil {
		respondError(w, http.StatusBadRequest, "env var "+key+" does not have dynamic options")
		return
	}

	options, err := h.options.GetOptionsForEnv(*envVar)
	if err != nil {
		h.log.Error("fetching dynamic options", "game", gameID, "key", key, "error", err)
		respondError(w, http.StatusInternalServerError, "failed to fetch options")
		return
	}

	respondOK(w, options)
}
