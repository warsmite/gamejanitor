package web

import (
	"crypto/rand"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/netinfo"
	"github.com/0xkowalskidev/gamejanitor/internal/service"
	"github.com/0xkowalskidev/gamejanitor/internal/web/handlers"
	"github.com/0xkowalskidev/gamejanitor/internal/web/static"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/csrf"
)

func NewRouter(
	gameStore *games.GameStore,
	gameserverSvc *service.GameserverService,
	consoleSvc *service.ConsoleService,
	fileSvc *service.FileService,
	scheduleSvc *service.ScheduleService,
	backupSvc *service.BackupService,
	querySvc *service.QueryService,
	settingsSvc *service.SettingsService,
	authSvc *service.AuthService,
	broadcaster *service.EventBroadcaster,
	netInfo *netinfo.Info,
	registry *worker.Registry,
	logPath string,
	dataDir string,
	sftpPort int,
	role string,
	log *slog.Logger,
) (http.Handler, error) {
	renderer, err := handlers.NewRenderer(netInfo, settingsSvc, sftpPort, role)
	if err != nil {
		return nil, fmt.Errorf("initializing template renderer: %w", err)
	}

	csrfKey, err := loadOrCreateCSRFKey(dataDir)
	if err != nil {
		return nil, fmt.Errorf("initializing CSRF key: %w", err)
	}

	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		renderer.RenderError(w, req, http.StatusNotFound)
	})

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Static files
	staticFS, _ := fs.Sub(static.Files, ".")
	r.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// Game assets served from the embedded/override game store
	r.Handle("/static/games/*", http.StripPrefix("/static/games/", http.FileServer(http.FS(gameStore.AssetsFS()))))

	// Auth middleware — applied to both API and page routes
	authMiddleware := AuthMiddleware(authSvc, settingsSvc)

	// API handlers (JSON) — no CSRF (uses JSON bodies, not forms)
	gameHandlers := handlers.NewGameHandlers(gameStore, log)
	minecraftVersions := handlers.NewMinecraftVersionsHandler(log)
	gameserverHandlers := handlers.NewGameserverHandlers(gameserverSvc, consoleSvc, querySvc, log)
	eventHandlers := handlers.NewEventHandlers(broadcaster, log)
	scheduleHandlers := handlers.NewScheduleHandlers(scheduleSvc, log)
	backupHandlers := handlers.NewBackupHandlers(backupSvc, log)
	logHandlers := handlers.NewLogHandlers(logPath, log)
	statusHandlers := handlers.NewStatusHandlers(gameserverSvc, querySvc, log)
	authHandlers := handlers.NewAuthHandlers(authSvc, log)
	workerHandlers := handlers.NewWorkerHandlers(registry, settingsSvc, gameserverSvc, log)
	settingsAPIHandlers := handlers.NewSettingsAPIHandlers(settingsSvc, log)

	requireAdmin := RequireAdmin(settingsSvc)
	requireAccess := RequireGameserverAccess(settingsSvc)
	requireStart := RequirePermission(settingsSvc, "start")
	requireStop := RequirePermission(settingsSvc, "stop")
	requireRestart := RequirePermission(settingsSvc, "restart")
	requireConsole := RequirePermission(settingsSvc, "console")
	requireFiles := RequirePermission(settingsSvc, "files")
	requireBackups := RequirePermission(settingsSvc, "backups")
	requireSettings := RequirePermission(settingsSvc, "settings")

	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)
		r.Use(authMiddleware)

		r.Get("/status", statusHandlers.Get)

		r.Route("/games", func(r chi.Router) {
			r.Get("/", gameHandlers.List)
			r.Get("/minecraft-java/versions", minecraftVersions.List)
			r.Get("/{id}", gameHandlers.Get)
		})

		r.Route("/gameservers", func(r chi.Router) {
			r.Get("/", gameserverHandlers.List)
			r.With(requireAdmin).Post("/", gameserverHandlers.Create)
			r.With(requireAdmin).Post("/bulk", gameserverHandlers.BulkAction)
			r.Route("/{id}", func(r chi.Router) {
				r.With(requireAccess).Get("/", gameserverHandlers.Get)
				r.With(requireSettings).Put("/", gameserverHandlers.Update)
				r.With(requireSettings).Delete("/", gameserverHandlers.Delete)
				r.With(requireStart).Post("/start", gameserverHandlers.Start)
				r.With(requireStop).Post("/stop", gameserverHandlers.Stop)
				r.With(requireRestart).Post("/restart", gameserverHandlers.Restart)
				r.With(requireSettings).Post("/update-game", gameserverHandlers.UpdateServerGame)
				r.With(requireSettings).Post("/reinstall", gameserverHandlers.Reinstall)
				r.With(requireAdmin).Post("/migrate", gameserverHandlers.Migrate)
				r.With(requireAccess).Get("/status", gameserverHandlers.Status)
				r.With(requireAccess).Get("/stats", gameserverHandlers.Stats)
				r.With(requireConsole).Get("/logs", gameserverHandlers.Logs)
				r.With(requireConsole).Post("/command", gameserverHandlers.SendCommand)

				r.Route("/schedules", func(r chi.Router) {
					r.Use(requireSettings)
					r.Get("/", scheduleHandlers.List)
					r.Post("/", scheduleHandlers.Create)
					r.Route("/{scheduleId}", func(r chi.Router) {
						r.Get("/", scheduleHandlers.Get)
						r.Put("/", scheduleHandlers.Update)
						r.Delete("/", scheduleHandlers.Delete)
					})
				})

				r.Route("/backups", func(r chi.Router) {
					r.Use(requireBackups)
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

		r.Route("/workers", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", workerHandlers.List)
			r.Route("/{workerID}", func(r chi.Router) {
				r.Get("/", workerHandlers.Get)
				r.Put("/port-range", workerHandlers.SetPortRange)
				r.Delete("/port-range", workerHandlers.ClearPortRange)
				r.Put("/limits", workerHandlers.SetLimits)
				r.Delete("/limits", workerHandlers.ClearLimits)
			})
		})

		r.Route("/settings", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", settingsAPIHandlers.Get)
			r.Put("/", settingsAPIHandlers.Update)
		})

		r.Route("/tokens", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", authHandlers.ListTokens)
			r.Post("/", authHandlers.CreateToken)
			r.Delete("/{tokenId}", authHandlers.DeleteToken)
		})

		r.Route("/worker-tokens", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", authHandlers.ListWorkerTokens)
			r.Post("/", authHandlers.CreateWorkerToken)
			r.Delete("/{tokenId}", authHandlers.DeleteWorkerToken)
		})
	})

	// CSRF middleware for page routes (HTML forms + HTMX)
	csrfMiddleware := csrf.Protect(
		csrfKey,
		csrf.Path("/"),
		csrf.Secure(false), // Allow HTTP for local dev; reverse proxy handles HTTPS in prod
		csrf.RequestHeader("X-CSRF-Token"),
	)
	// gorilla/csrf defaults to HTTPS when checking Origin headers.
	// Mark plaintext requests so the origin check uses http:// scheme,
	// otherwise Origin: http://localhost mismatches https://localhost → 403.
	plaintextMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil {
				r = csrf.PlaintextHTTPRequest(r)
			}
			next.ServeHTTP(w, r)
		})
	}

	// Auth page handlers — login/logout outside auth middleware
	pageAuth := handlers.NewPageAuthHandlers(authSvc, settingsSvc, gameserverSvc, renderer, log)
	r.Group(func(r chi.Router) {
		r.Use(plaintextMiddleware)
		r.Use(csrfMiddleware)
		r.Get("/login", pageAuth.LoginPage)
		r.Post("/login", pageAuth.Login)
		r.Post("/logout", pageAuth.Logout)
	})

	// Page handlers (HTML)
	pageDashboard := handlers.NewPageDashboardHandlers(gameStore, gameserverSvc, querySvc, settingsSvc, registry, renderer, log)
	pageGames := handlers.NewPageGameHandlers(gameStore, gameserverSvc, renderer, log)
	pageGameservers := handlers.NewPageGameserverHandlers(gameStore, gameserverSvc, querySvc, settingsSvc, registry, renderer, log)
	pageSettings := handlers.NewPageSettingsHandlers(settingsSvc, authSvc, registry, renderer, log)
	pageActions := handlers.NewPageActionHandlers(gameStore, gameserverSvc, renderer, log)
	pageConsole := handlers.NewPageConsoleHandlers(consoleSvc, gameStore, gameserverSvc, renderer, log)
	pageFiles := handlers.NewPageFileHandlers(fileSvc, gameStore, gameserverSvc, renderer, log)
	pageSchedules := handlers.NewPageScheduleHandlers(scheduleSvc, gameStore, gameserverSvc, renderer, log)
	pageBackups := handlers.NewPageBackupHandlers(backupSvc, gameStore, gameserverSvc, renderer, log)

	r.Group(func(r chi.Router) {
		r.Use(plaintextMiddleware)
		r.Use(csrfMiddleware)
		r.Use(authMiddleware)

		r.Get("/", pageDashboard.Dashboard)
		r.Get("/dashboard/workers", pageDashboard.WorkersPartial)

		r.Route("/games", func(r chi.Router) {
			r.Get("/", pageGames.List)
			r.Get("/{id}", pageGames.Detail)
		})

		// Settings — admin only
		r.Route("/settings", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", pageSettings.SettingsPage)
			r.Get("/workers", pageSettings.WorkersPartial)
			r.Post("/connection-address", pageSettings.SetConnectionAddress)
			r.Delete("/connection-address", pageSettings.ClearConnectionAddress)
			r.Post("/port-range", pageSettings.SavePortRange)
			r.Post("/port-mode", pageSettings.SavePortMode)
			r.Post("/max-backups", pageSettings.SaveMaxBackups)
			r.Post("/localhost-bypass/enable", pageSettings.SetLocalhostBypass(true))
			r.Post("/localhost-bypass/disable", pageSettings.SetLocalhostBypass(false))
			r.Post("/workers/{workerID}/port-range", pageSettings.SaveWorkerPortRange)
			r.Delete("/workers/{workerID}/port-range", pageSettings.ClearWorkerPortRange)
			r.Post("/workers/{workerID}/limits", pageSettings.SaveWorkerLimits)
			r.Delete("/workers/{workerID}/limits", pageSettings.ClearWorkerLimits)
			r.Post("/worker-tokens", pageSettings.CreateWorkerToken)
			r.Delete("/worker-tokens/{tokenId}", pageSettings.DeleteWorkerToken)
			r.Get("/tokens", pageAuth.TokensPage)
			r.Post("/tokens", pageAuth.CreateToken)
			r.Delete("/tokens/{tokenId}", pageAuth.DeleteToken)
			r.Post("/auth/enable", pageAuth.EnableAuth)
			r.Post("/auth/disable", pageAuth.DisableAuth)
		})

		r.Route("/gameservers", func(r chi.Router) {
			r.With(requireAdmin).Get("/new", pageGameservers.New)
			r.With(requireAdmin).Post("/", pageGameservers.Create)
			r.Route("/{id}", func(r chi.Router) {
				// View access
				r.With(requireAccess).Get("/", pageGameservers.Detail)
				r.With(requireAccess).Get("/card", pageGameservers.Card)

				// Settings permission
				r.With(requireSettings).Get("/edit", pageGameservers.Edit)
				r.With(requireSettings).Put("/", pageGameservers.Update)
				r.With(requireSettings).Delete("/", pageGameservers.Delete)

				// Lifecycle actions
				r.With(requireStart).Post("/start", pageActions.Start)
				r.With(requireStop).Post("/stop", pageActions.Stop)
				r.With(requireRestart).Post("/restart", pageActions.Restart)
				r.With(requireSettings).Post("/update-game", pageActions.UpdateGame)
				r.With(requireSettings).Post("/reinstall", pageActions.Reinstall)

				// Console
				r.With(requireConsole).Get("/console", pageConsole.Console)
				r.With(requireConsole).Get("/console/stream", pageConsole.LogStream)
				r.With(requireConsole).Get("/console/sessions", pageConsole.Sessions)
				r.With(requireConsole).Post("/console/command", pageConsole.SendCommand)

				// Files
				r.With(requireFiles).Get("/files", pageFiles.List)
				r.With(requireFiles).Get("/files/list", pageFiles.ListJSON)
				r.With(requireFiles).Get("/files/content", pageFiles.ReadFile)
				r.With(requireFiles).Put("/files/content", pageFiles.WriteFile)
				r.With(requireFiles).Delete("/files/entry", pageFiles.DeletePath)
				r.With(requireFiles).Post("/files/mkdir", pageFiles.CreateDirectory)
				r.With(requireFiles).Get("/files/download", pageFiles.DownloadFile)
				r.With(requireFiles).Post("/files/upload", pageFiles.UploadFile)
				r.With(requireFiles).Post("/files/rename", pageFiles.RenamePath)

				// Schedules
				r.With(requireSettings).Get("/schedules", pageSchedules.List)
				r.With(requireSettings).Post("/schedules", pageSchedules.Create)
				r.With(requireSettings).Put("/schedules/{scheduleId}", pageSchedules.Update)
				r.With(requireSettings).Delete("/schedules/{scheduleId}", pageSchedules.Delete)
				r.With(requireSettings).Post("/schedules/{scheduleId}/toggle", pageSchedules.Toggle)

				// Backups
				r.With(requireBackups).Get("/backups", pageBackups.List)
				r.With(requireBackups).Post("/backups", pageBackups.Create)
				r.With(requireBackups).Post("/backups/{backupId}/restore", pageBackups.Restore)
				r.With(requireBackups).Delete("/backups/{backupId}", pageBackups.Delete)
			})
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

func loadOrCreateCSRFKey(dataDir string) ([]byte, error) {
	keyPath := filepath.Join(dataDir, "csrf.key")
	key, err := os.ReadFile(keyPath)
	if err == nil && len(key) == 32 {
		return key, nil
	}

	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating CSRF key: %w", err)
	}

	if err := os.WriteFile(keyPath, key, 0600); err != nil {
		return nil, fmt.Errorf("writing CSRF key: %w", err)
	}

	return key, nil
}
