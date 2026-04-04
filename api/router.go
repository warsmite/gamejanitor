package api

import (
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/controller/backup"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/controller/schedule"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/warsmite/gamejanitor/api/handler"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type RouterOptions struct {
	Config          config.Config
	Role            string // "standalone", "controller", "controller+worker"
	LogPath         string
	GameStore       *games.GameStore
	GameserverSvc   *gameserver.GameserverService
	ConsoleSvc      *gameserver.ConsoleService
	FileSvc         *gameserver.FileService
	ScheduleSvc     *schedule.ScheduleService
	BackupSvc       *backup.BackupService
	QuerySvc        *status.QueryService
	StatsPoller     *status.StatsPoller
	SettingsSvc     *settings.SettingsService
	AuthSvc         *auth.AuthService
	ModSvc          *mod.ModService
	WorkerNodeSvc   *orchestrator.WorkerNodeService
	WebhookSvc      *webhook.WebhookEndpointService
	EventHistorySvc *event.EventHistoryService
	ActivityStore    handler.EventStore
	StatsHistory     handler.StatsHistoryQuerier
	AccessChecker GameserverAccessChecker
	QuotaQuerier     handler.QuotaQuerier
	Broadcaster      *controller.EventBus
	Log             *slog.Logger
	WebUI           fs.FS // embedded UI static files (nil to disable)
}

// Router wraps the HTTP handler and background resources that need cleanup.
type Router struct {
	http.Handler
	rateLimiter *RateLimitStore
}

// Stop cleans up background goroutines owned by the router.
func (rt *Router) Stop() {
	rt.rateLimiter.Stop()
}

func NewRouter(opts RouterOptions) *Router {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)
	r.Use(securityHeaders)

	rateLimitStore := NewRateLimitStore(opts.SettingsSvc, opts.Log)
	r.Use(rateLimitStore.PerIPMiddleware())

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Game assets (icons) served from the game store
	r.Handle("/assets/games/*", http.StripPrefix("/assets/games/", http.FileServer(http.FS(opts.GameStore.AssetsFS()))))

	authMiddleware := AuthMiddleware(opts.AuthSvc, opts.SettingsSvc)

	optionsRegistry := games.NewOptionsRegistry(opts.Log)
	gameHandlers := handler.NewGameHandlers(opts.GameStore, optionsRegistry, opts.Log)
	gameserverHandlers := handler.NewGameserverHandlers(opts.GameserverSvc, opts.ConsoleSvc, opts.QuerySvc, opts.StatsPoller, opts.StatsHistory, opts.Log)
	eventHandlers := handler.NewEventHandlers(opts.Broadcaster, opts.EventHistorySvc, opts.Log)
	scheduleHandlers := handler.NewScheduleHandlers(opts.ScheduleSvc, opts.Log)
	backupHandlers := handler.NewBackupHandlers(opts.BackupSvc, opts.Log)
	fileHandlers := handler.NewFileHandlers(opts.FileSvc, opts.Log)
	logHandlers := handler.NewLogHandlers(opts.LogPath, opts.Log)
	authHandlers := handler.NewAuthHandlers(opts.AuthSvc, opts.QuotaQuerier, opts.Log)
	workerHandlers := handler.NewWorkerHandlers(opts.WorkerNodeSvc, opts.Log)
	statusHandlers := handler.NewStatusHandlers(opts.GameserverSvc, opts.QuerySvc, opts.WorkerNodeSvc, opts.Config, opts.Log)
	settingsAPIHandlers := handler.NewSettingsAPIHandlers(opts.SettingsSvc, opts.AuthSvc, opts.Log)
	webhookHandlers := handler.NewWebhookHandlers(opts.WebhookSvc, opts.Log)
	modHandlers := handler.NewModHandlers(opts.ModSvc, opts.Log)
	activityHandlers := handler.NewActivityHandlers(opts.ActivityStore)

	ac := opts.AccessChecker
	requireAdmin := RequireAdmin(opts.SettingsSvc)
	requireAccess := RequireGameserverAccess(opts.SettingsSvc, ac)
	requireStart := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverStart)
	requireStop := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverStop)
	requireRestart := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverRestart)
	requireUpdateGame := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverUpdateGame)
	requireReinstall := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverReinstall)
	requireDelete := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverDelete)
	requireArchive := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverArchive)
	requireUnarchive := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverUnarchive)
	requireRegenSFTP := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverRegenerateSFTP)
	requireLogs := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverLogs)
	requireCommands := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverCommand)
	requireFilesRead := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverFilesRead)
	requireFilesWrite := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverFilesWrite)
	requireBackupRead := RequirePermission(opts.SettingsSvc, ac, auth.PermBackupRead)
	requireBackupCreate := RequirePermission(opts.SettingsSvc, ac, auth.PermBackupCreate)
	requireBackupDelete := RequirePermission(opts.SettingsSvc, ac, auth.PermBackupDelete)
	requireBackupRestore := RequirePermission(opts.SettingsSvc, ac, auth.PermBackupRestore)
	requireBackupDownload := RequirePermission(opts.SettingsSvc, ac, auth.PermBackupDownload)
	requireScheduleRead := RequirePermission(opts.SettingsSvc, ac, auth.PermScheduleRead)
	requireScheduleCreate := RequirePermission(opts.SettingsSvc, ac, auth.PermScheduleCreate)
	requireScheduleUpdate := RequirePermission(opts.SettingsSvc, ac, auth.PermScheduleUpdate)
	requireScheduleDelete := RequirePermission(opts.SettingsSvc, ac, auth.PermScheduleDelete)
	requireModsRead := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverModsRead)
	requireModsWrite := RequirePermission(opts.SettingsSvc, ac, auth.PermGameserverModsWrite)

	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)
		r.Use(authMiddleware)
		r.Use(rateLimitStore.PerTokenMiddleware())

		r.Get("/status", statusHandlers.Get)
		r.Get("/me", authHandlers.Me)

		r.Route("/games", func(r chi.Router) {
			r.Get("/", gameHandlers.List)
			r.Get("/{id}", gameHandlers.Get)
			r.Get("/{id}/options/{key}", gameHandlers.Options)
		})

		r.Route("/gameservers", func(r chi.Router) {
			r.Get("/", gameserverHandlers.List)
			r.With(RequireClusterPermission(opts.SettingsSvc, auth.PermGameserverCreate)).Post("/", gameserverHandlers.Create)
			r.With(requireAdmin).Post("/bulk", gameserverHandlers.BulkAction)
			r.Route("/{id}", func(r chi.Router) {
				r.With(requireAccess).Get("/", gameserverHandlers.Get)
				r.With(requireAccess).Patch("/", gameserverHandlers.Update)
				r.With(requireDelete).Delete("/", gameserverHandlers.Delete)
				r.With(requireStart).Post("/start", gameserverHandlers.Start)
				r.With(requireStop).Post("/stop", gameserverHandlers.Stop)
				r.With(requireRestart).Post("/restart", gameserverHandlers.Restart)
				r.With(requireUpdateGame).Post("/update-game", gameserverHandlers.UpdateServerGame)
				r.With(requireReinstall).Post("/reinstall", gameserverHandlers.Reinstall)
				r.With(requireArchive).Post("/archive", gameserverHandlers.Archive)
				r.With(requireUnarchive).Post("/unarchive", gameserverHandlers.Unarchive)
				r.With(requireAdmin).Post("/migrate", gameserverHandlers.Migrate)
				r.With(requireRegenSFTP).Post("/regenerate-sftp-password", gameserverHandlers.RegenerateSFTPPassword)
				r.With(requireAccess).Get("/status", gameserverHandlers.Status)
				r.With(requireAccess).Get("/operation", gameserverHandlers.OperationStream)
				r.With(requireAccess).Get("/query", gameserverHandlers.Query)
				r.With(requireAccess).Get("/stats", gameserverHandlers.Stats)
				r.With(requireAccess).Get("/stats/history", gameserverHandlers.StatsHistory)
				r.With(requireLogs).Get("/logs", gameserverHandlers.Logs)
				r.With(requireLogs).Get("/logs/sessions", gameserverHandlers.LogSessions)
				r.With(requireLogs).Get("/logs/stream", gameserverHandlers.StreamLogs)
				r.With(requireCommands).Post("/command", gameserverHandlers.SendCommand)

				r.Route("/schedules", func(r chi.Router) {
					r.With(requireScheduleRead).Get("/", scheduleHandlers.List)
					r.With(requireScheduleCreate).Post("/", scheduleHandlers.Create)
					r.Route("/{scheduleId}", func(r chi.Router) {
						r.With(requireScheduleRead).Get("/", scheduleHandlers.Get)
						r.With(requireScheduleUpdate).Patch("/", scheduleHandlers.Update)
						r.With(requireScheduleDelete).Delete("/", scheduleHandlers.Delete)
					})
				})

				r.Route("/backups", func(r chi.Router) {
					r.With(requireBackupRead).Get("/", backupHandlers.List)
					r.With(requireBackupCreate).Post("/", backupHandlers.Create)
					r.Route("/{backupId}", func(r chi.Router) {
						r.With(requireBackupDownload).Get("/download", backupHandlers.Download)
						r.With(requireBackupRestore).Post("/restore", backupHandlers.Restore)
						r.With(requireBackupDelete).Delete("/", backupHandlers.Delete)
					})
				})

				r.Route("/files", func(r chi.Router) {
					r.With(requireFilesRead).Get("/", fileHandlers.List)
					r.With(requireFilesRead).Get("/content", fileHandlers.Read)
					r.With(requireFilesWrite).Put("/content", fileHandlers.Write)
					r.With(requireFilesWrite).Delete("/", fileHandlers.Delete)
					r.With(requireFilesRead).Get("/download", fileHandlers.Download)
					r.With(requireFilesWrite).Post("/upload", fileHandlers.Upload)
					r.With(requireFilesWrite).Post("/rename", fileHandlers.Rename)
					r.With(requireFilesWrite).Post("/mkdir", fileHandlers.CreateDirectory)
				})

				r.Route("/mods", func(r chi.Router) {
					r.With(requireModsRead).Get("/", modHandlers.List)
					r.With(requireModsRead).Get("/config", modHandlers.Config)
					r.With(requireModsRead).Get("/search", modHandlers.Search)
					r.With(requireModsRead).Get("/versions", modHandlers.Versions)
					r.With(requireModsRead).Get("/updates", modHandlers.CheckUpdates)
					r.With(requireModsRead).Post("/check-compatibility", modHandlers.CheckCompatibility)
					r.With(requireModsWrite).Post("/", modHandlers.Install)
					r.With(requireModsWrite).Post("/pack", modHandlers.InstallPack)
					r.With(requireModsWrite).Post("/url", modHandlers.InstallURL)
					r.With(requireModsWrite).Post("/upload", modHandlers.Upload)
					r.With(requireModsRead).Post("/scan", modHandlers.Scan)
					r.With(requireModsWrite).Post("/track", modHandlers.TrackFile)
					r.With(requireModsWrite).Post("/update-all", modHandlers.UpdateAll)
					r.With(requireModsWrite).Post("/{modId}/update", modHandlers.Update)
					r.With(requireModsWrite).Post("/{modId}/update-pack", modHandlers.UpdatePack)
					r.With(requireModsWrite).Delete("/{modId}", modHandlers.Uninstall)
				})
			})
		})

		r.Get("/logs", logHandlers.Get)
		r.Get("/events", eventHandlers.SSE)
		r.Get("/events/history", eventHandlers.History)
		r.Get("/activity", activityHandlers.List)

		r.Route("/workers", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", workerHandlers.List)
			r.Route("/{workerID}", func(r chi.Router) {
				r.Get("/", workerHandlers.Get)
				r.Patch("/", workerHandlers.Update)
			})
		})

		r.Route("/settings", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", settingsAPIHandlers.Get)
			r.Patch("/", settingsAPIHandlers.Update)
		})

		r.Route("/webhooks", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", webhookHandlers.List)
			r.Post("/", webhookHandlers.Create)
			r.Get("/{webhookId}", webhookHandlers.Get)
			r.Patch("/{webhookId}", webhookHandlers.Update)
			r.Delete("/{webhookId}", webhookHandlers.Delete)
			r.Post("/{webhookId}/test", webhookHandlers.Test)
			r.Get("/{webhookId}/deliveries", webhookHandlers.Deliveries)
		})

		r.Route("/tokens", func(r chi.Router) {
			r.Use(requireAdmin)
			r.Get("/", authHandlers.ListTokens)
			r.Post("/", authHandlers.CreateToken)
			r.Delete("/{tokenId}", authHandlers.DeleteToken)
			r.Post("/{tokenId}/rotate", authHandlers.RotateToken)
			r.Post("/{tokenId}/claim-code", authHandlers.GenerateClaimCode)
		})
	})

	// Public claim code redemption — no auth required
	r.Post("/api/claim", authHandlers.RedeemClaimCode)

	// Serve embedded web UI (SPA with index.html fallback)
	if opts.WebUI != nil {
		r.Get("/*", spaHandler(opts.WebUI))
	}

	return &Router{Handler: r, rateLimiter: rateLimitStore}
}

// spaHandler serves static files from the embedded FS, falling back to
// index.html for any path that doesn't match a file (client-side routing).
func spaHandler(uiFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(uiFS))
	return func(w http.ResponseWriter, r *http.Request) {
		// Try to serve the exact file
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else if path[0] == '/' {
			path = path[1:]
		}

		// Check if file exists
		if _, err := fs.Stat(uiFS, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fall back to index.html for SPA routing
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

// securityHeaders sets standard protective headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: https://cdn.modrinth.com https://*.umod.org https://steamuserimages-a.akamaihd.net https://*.steamusercontent.com; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
