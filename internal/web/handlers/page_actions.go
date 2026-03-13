package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/go-chi/chi/v5"
)

type PageActionHandlers struct {
	gameSvc       *service.GameService
	gameserverSvc *service.GameserverService
	renderer      *Renderer
	log           *slog.Logger
}

func NewPageActionHandlers(gameSvc *service.GameService, gameserverSvc *service.GameserverService, renderer *Renderer, log *slog.Logger) *PageActionHandlers {
	return &PageActionHandlers{gameSvc: gameSvc, gameserverSvc: gameserverSvc, renderer: renderer, log: log}
}

func (h *PageActionHandlers) Start(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(ctx context.Context, id string) error { return h.gameserverSvc.Start(ctx, id) })
}

func (h *PageActionHandlers) Stop(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(ctx context.Context, id string) error { return h.gameserverSvc.Stop(ctx, id) })
}

func (h *PageActionHandlers) Restart(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(ctx context.Context, id string) error { return h.gameserverSvc.Restart(ctx, id) })
}

func (h *PageActionHandlers) UpdateGame(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(ctx context.Context, id string) error { return h.gameserverSvc.UpdateServerGame(ctx, id) })
}

func (h *PageActionHandlers) Reinstall(w http.ResponseWriter, r *http.Request) {
	h.doAction(w, r, func(ctx context.Context, id string) error { return h.gameserverSvc.Reinstall(ctx, id) })
}

// doAction kicks off a lifecycle action in the background and returns immediately.
// Status updates reach the client via SSE.
func (h *PageActionHandlers) doAction(w http.ResponseWriter, r *http.Request, action func(context.Context, string) error) {
	id := chi.URLParam(r, "id")

	go func() {
		// Detach from the request context so the action isn't cancelled when the HTTP response is sent.
		ctx := context.Background()
		if err := action(ctx, id); err != nil {
			h.log.Error("gameserver action failed", "id", id, "error", err)
		}
	}()

	w.WriteHeader(http.StatusNoContent)
}
