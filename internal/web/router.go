package web

import (
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/web/handlers"
	"github.com/0xkowalskidev/gamejanitor/internal/web/static"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func NewRouter(
	gameSvc *service.GameService,
	gameserverSvc *service.GameserverService,
	consoleSvc *service.ConsoleService,
	dockerClient *docker.Client,
	broadcaster *service.EventBroadcaster,
	log *slog.Logger,
) (http.Handler, error) {
	renderer, err := handlers.NewRenderer()
	if err != nil {
		return nil, fmt.Errorf("initializing template renderer: %w", err)
	}

	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Static files
	staticFS, _ := fs.Sub(static.Files, ".")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// API handlers (JSON)
	gameHandlers := handlers.NewGameHandlers(gameSvc, log)
	gameserverHandlers := handlers.NewGameserverHandlers(gameserverSvc, dockerClient, log)
	eventHandlers := handlers.NewEventHandlers(broadcaster, log)

	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)

		r.Route("/games", func(r chi.Router) {
			r.Get("/", gameHandlers.List)
			r.Post("/", gameHandlers.Create)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", gameHandlers.Get)
				r.Put("/", gameHandlers.Update)
				r.Delete("/", gameHandlers.Delete)
			})
		})

		r.Route("/gameservers", func(r chi.Router) {
			r.Get("/", gameserverHandlers.List)
			r.Post("/", gameserverHandlers.Create)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", gameserverHandlers.Get)
				r.Put("/", gameserverHandlers.Update)
				r.Delete("/", gameserverHandlers.Delete)
				r.Post("/start", gameserverHandlers.Start)
				r.Post("/stop", gameserverHandlers.Stop)
				r.Post("/restart", gameserverHandlers.Restart)
				r.Post("/update-game", gameserverHandlers.UpdateServerGame)
				r.Post("/reinstall", gameserverHandlers.Reinstall)
				r.Get("/status", gameserverHandlers.Status)
			})
		})

		r.Get("/events", eventHandlers.SSE)
	})

	// Page handlers (HTML)
	pageDashboard := handlers.NewPageDashboardHandlers(gameSvc, gameserverSvc, renderer, log)
	pageGames := handlers.NewPageGameHandlers(gameSvc, gameserverSvc, renderer, log)
	pageGameservers := handlers.NewPageGameserverHandlers(gameSvc, gameserverSvc, renderer, log)
	pageActions := handlers.NewPageActionHandlers(gameSvc, gameserverSvc, renderer, log)
	pageConsole := handlers.NewPageConsoleHandlers(consoleSvc, gameSvc, gameserverSvc, renderer, log)

	r.Get("/", pageDashboard.Dashboard)

	r.Route("/games", func(r chi.Router) {
		r.Get("/", pageGames.List)
		r.Get("/new", pageGames.New)
		r.Post("/", pageGames.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", pageGames.Detail)
			r.Get("/edit", pageGames.Edit)
			r.Put("/", pageGames.Update)
			r.Delete("/", pageGames.Delete)
		})
	})

	r.Route("/gameservers", func(r chi.Router) {
		r.Get("/new", pageGameservers.New)
		r.Post("/", pageGameservers.Create)
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", pageGameservers.Detail)
			r.Get("/edit", pageGameservers.Edit)
			r.Put("/", pageGameservers.Update)
			r.Delete("/", pageGameservers.Delete)
			r.Get("/card", pageGameservers.Card)
			r.Post("/start", pageActions.Start)
			r.Post("/stop", pageActions.Stop)
			r.Post("/restart", pageActions.Restart)
			r.Post("/update-game", pageActions.UpdateGame)
			r.Post("/reinstall", pageActions.Reinstall)
			r.Get("/console", pageConsole.Console)
			r.Get("/console/stream", pageConsole.LogStream)
			r.Post("/console/command", pageConsole.SendCommand)
		})
	})

	return r, nil
}

func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
