package handlers

import (
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/go-chi/chi/v5"
)

type GameHandlers struct {
	store *games.GameStore
	log   *slog.Logger
}

func NewGameHandlers(store *games.GameStore, log *slog.Logger) *GameHandlers {
	return &GameHandlers{store: store, log: log}
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
