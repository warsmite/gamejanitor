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
	fileSvc *service.FileService,
	scheduleSvc *service.ScheduleService,
	backupSvc *service.BackupService,
	querySvc *service.QueryService,
	dockerClient *docker.Client,
	broadcaster *service.EventBroadcaster,
	logPath string,
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
	gameserverHandlers := handlers.NewGameserverHandlers(gameserverSvc, consoleSvc, querySvc, dockerClient, log)
	eventHandlers := handlers.NewEventHandlers(broadcaster, log)
	scheduleHandlers := handlers.NewScheduleHandlers(scheduleSvc, log)
	backupHandlers := handlers.NewBackupHandlers(backupSvc, log)
	logHandlers := handlers.NewLogHandlers(logPath, log)
	statusHandlers := handlers.NewStatusHandlers(gameserverSvc, querySvc, dockerClient, log)

	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)

		r.Get("/status", statusHandlers.Get)

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
				r.Get("/logs", gameserverHandlers.Logs)
				r.Post("/command", gameserverHandlers.SendCommand)

				r.Route("/schedules", func(r chi.Router) {
					r.Get("/", scheduleHandlers.List)
					r.Post("/", scheduleHandlers.Create)
					r.Route("/{scheduleId}", func(r chi.Router) {
						r.Get("/", scheduleHandlers.Get)
						r.Put("/", scheduleHandlers.Update)
						r.Delete("/", scheduleHandlers.Delete)
					})
				})

				r.Route("/backups", func(r chi.Router) {
					r.Get("/", backupHandlers.List)
					r.Post("/", backupHandlers.Create)
					r.Route("/{backupId}", func(r chi.Router) {
						r.Post("/restore", backupHandlers.Restore)
						r.Delete("/", backupHandlers.Delete)
					})
				})
			})
		})

		r.Get("/logs", logHandlers.Get)
		r.Get("/events", eventHandlers.SSE)
	})

	// Page handlers (HTML)
	pageDashboard := handlers.NewPageDashboardHandlers(gameSvc, gameserverSvc, querySvc, renderer, log)
	pageGames := handlers.NewPageGameHandlers(gameSvc, gameserverSvc, renderer, log)
	pageGameservers := handlers.NewPageGameserverHandlers(gameSvc, gameserverSvc, querySvc, renderer, log)
	pageActions := handlers.NewPageActionHandlers(gameSvc, gameserverSvc, renderer, log)
	pageConsole := handlers.NewPageConsoleHandlers(consoleSvc, gameSvc, gameserverSvc, renderer, log)
	pageFiles := handlers.NewPageFileHandlers(fileSvc, gameSvc, gameserverSvc, renderer, log)
	pageSchedules := handlers.NewPageScheduleHandlers(scheduleSvc, gameSvc, gameserverSvc, renderer, log)
	pageBackups := handlers.NewPageBackupHandlers(backupSvc, gameSvc, gameserverSvc, renderer, log)

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
			r.Get("/files", pageFiles.List)
			r.Get("/files/list", pageFiles.ListJSON)
			r.Get("/files/content", pageFiles.ReadFile)
			r.Put("/files/content", pageFiles.WriteFile)
			r.Delete("/files/entry", pageFiles.DeletePath)
			r.Post("/files/mkdir", pageFiles.CreateDirectory)
			r.Get("/schedules", pageSchedules.List)
			r.Post("/schedules", pageSchedules.Create)
			r.Put("/schedules/{scheduleId}", pageSchedules.Update)
			r.Delete("/schedules/{scheduleId}", pageSchedules.Delete)
			r.Post("/schedules/{scheduleId}/toggle", pageSchedules.Toggle)
			r.Get("/backups", pageBackups.List)
			r.Post("/backups", pageBackups.Create)
			r.Post("/backups/{backupId}/restore", pageBackups.Restore)
			r.Delete("/backups/{backupId}", pageBackups.Delete)
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
