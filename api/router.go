package api

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/api/handlers"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

type RouterOptions struct {
	Config        config.Config
	Role          string // "standalone", "controller", "controller+worker"
	LogPath       string
	GameStore     *games.GameStore
	GameserverSvc *service.GameserverService
	ConsoleSvc    *service.ConsoleService
	FileSvc       *service.FileService
	ScheduleSvc   *service.ScheduleService
	BackupSvc     *service.BackupService
	QuerySvc      *service.QueryService
	SettingsSvc   *service.SettingsService
	AuthSvc       *service.AuthService
	Broadcaster   *service.EventBus
	Registry      *worker.Registry
	DB            *sql.DB
	Log           *slog.Logger
}

func NewRouter(opts RouterOptions) http.Handler {
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
	r.Handle("/games/*", http.StripPrefix("/games/", http.FileServer(http.FS(opts.GameStore.AssetsFS()))))

	authMiddleware := AuthMiddleware(opts.AuthSvc, opts.SettingsSvc)

	optionsRegistry := games.NewOptionsRegistry(opts.Log)
	gameHandlers := handlers.NewGameHandlers(opts.GameStore, optionsRegistry, opts.Log)
	gameserverHandlers := handlers.NewGameserverHandlers(opts.GameserverSvc, opts.ConsoleSvc, opts.QuerySvc, opts.Log)
	eventHistorySvc := service.NewEventHistoryService(opts.DB)
	eventHandlers := handlers.NewEventHandlers(opts.Broadcaster, eventHistorySvc, opts.Log)
	scheduleHandlers := handlers.NewScheduleHandlers(opts.ScheduleSvc, opts.Log)
	backupHandlers := handlers.NewBackupHandlers(opts.BackupSvc, opts.Log)
	fileHandlers := handlers.NewFileHandlers(opts.FileSvc, opts.Log)
	logHandlers := handlers.NewLogHandlers(opts.LogPath, opts.Log)
	statusHandlers := handlers.NewStatusHandlers(opts.GameserverSvc, opts.QuerySvc, opts.Log)
	authHandlers := handlers.NewAuthHandlers(opts.AuthSvc, opts.Log)
	workerNodeSvc := service.NewWorkerNodeService(opts.DB, opts.Registry, opts.Log)
	workerHandlers := handlers.NewWorkerHandlers(workerNodeSvc, opts.Log)
	settingsAPIHandlers := handlers.NewSettingsAPIHandlers(opts.SettingsSvc, opts.Log)
	webhookSvc := service.NewWebhookEndpointService(opts.DB, opts.Log)
	webhookHandlers := handlers.NewWebhookHandlers(webhookSvc, opts.Log)

	requireAdmin := RequireAdmin(opts.SettingsSvc)
	requireAccess := RequireGameserverAccess(opts.SettingsSvc)
	requireStart := RequirePermission(opts.SettingsSvc, service.PermGameserverStart)
	requireStop := RequirePermission(opts.SettingsSvc, service.PermGameserverStop)
	requireRestart := RequirePermission(opts.SettingsSvc, service.PermGameserverRestart)
	requireLogs := RequirePermission(opts.SettingsSvc, service.PermGameserverLogs)
	requireCommands := RequirePermission(opts.SettingsSvc, service.PermGameserverCommand)
	requireFilesRead := RequirePermission(opts.SettingsSvc, service.PermGameserverFilesRead)
	requireFilesWrite := RequirePermission(opts.SettingsSvc, service.PermGameserverFilesWrite)
	requireBackupCreate := RequirePermission(opts.SettingsSvc, service.PermBackupCreate)
	requireBackupDelete := RequirePermission(opts.SettingsSvc, service.PermBackupDelete)
	requireBackupRestore := RequirePermission(opts.SettingsSvc, service.PermBackupRestore)
	requireBackupDownload := RequirePermission(opts.SettingsSvc, service.PermBackupDownload)
	requireScheduleCreate := RequirePermission(opts.SettingsSvc, service.PermScheduleCreate)
	requireScheduleUpdate := RequirePermission(opts.SettingsSvc, service.PermScheduleUpdate)
	requireScheduleDelete := RequirePermission(opts.SettingsSvc, service.PermScheduleDelete)
	requireConfigure := RequirePermission(opts.SettingsSvc, service.PermGameserverEditEnv)
	requireDelete := RequirePermission(opts.SettingsSvc, service.PermGameserverDelete)

	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)
		r.Use(authMiddleware)
		r.Use(rateLimitStore.PerTokenMiddleware())

		r.Get("/status", statusHandlers.Get)

		r.Route("/games", func(r chi.Router) {
			r.Get("/", gameHandlers.List)
			r.Get("/{id}", gameHandlers.Get)
			r.Get("/{id}/options/{key}", gameHandlers.Options)
		})

		r.Route("/gameservers", func(r chi.Router) {
			r.Get("/", gameserverHandlers.List)
			r.With(requireAdmin).Post("/", gameserverHandlers.Create)
			r.With(requireAdmin).Post("/bulk", gameserverHandlers.BulkAction)
			r.Route("/{id}", func(r chi.Router) {
				r.With(requireAccess).Get("/", gameserverHandlers.Get)
				r.With(requireConfigure).Patch("/", gameserverHandlers.Update)
				r.With(requireDelete).Delete("/", gameserverHandlers.Delete)
				r.With(requireStart).Post("/start", gameserverHandlers.Start)
				r.With(requireStop).Post("/stop", gameserverHandlers.Stop)
				r.With(requireRestart).Post("/restart", gameserverHandlers.Restart)
				r.With(requireConfigure).Post("/update-game", gameserverHandlers.UpdateServerGame)
				r.With(requireConfigure).Post("/reinstall", gameserverHandlers.Reinstall)
				r.With(requireAdmin).Post("/migrate", gameserverHandlers.Migrate)
				r.With(requireAdmin).Post("/regenerate-sftp-password", gameserverHandlers.RegenerateSFTPPassword)
				r.With(requireAccess).Get("/status", gameserverHandlers.Status)
				r.With(requireAccess).Get("/query", gameserverHandlers.Query)
				r.With(requireAccess).Get("/stats", gameserverHandlers.Stats)
				r.With(requireLogs).Get("/logs", gameserverHandlers.Logs)
				r.With(requireCommands).Post("/command", gameserverHandlers.SendCommand)

				r.Route("/schedules", func(r chi.Router) {
					r.With(requireScheduleCreate).Get("/", scheduleHandlers.List)
					r.With(requireScheduleCreate).Post("/", scheduleHandlers.Create)
					r.Route("/{scheduleId}", func(r chi.Router) {
						r.With(requireScheduleCreate).Get("/", scheduleHandlers.Get)
						r.With(requireScheduleUpdate).Patch("/", scheduleHandlers.Update)
						r.With(requireScheduleDelete).Delete("/", scheduleHandlers.Delete)
					})
				})

				r.Route("/backups", func(r chi.Router) {
					r.With(requireBackupCreate).Get("/", backupHandlers.List)
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
			})
		})

		r.Get("/logs", logHandlers.Get)
		r.Get("/events", eventHandlers.SSE)
		r.Get("/events/history", eventHandlers.History)

		r.Route("/workers", func(r chi.Router) {
			r.Use(RequireClusterPermission(opts.SettingsSvc, service.PermNodesManage))
			r.Get("/", workerHandlers.List)
			r.Route("/{workerID}", func(r chi.Router) {
				r.Get("/", workerHandlers.Get)
				r.Patch("/port-range", workerHandlers.SetPortRange)
				r.Delete("/port-range", workerHandlers.ClearPortRange)
				r.Patch("/limits", workerHandlers.SetLimits)
				r.Delete("/limits", workerHandlers.ClearLimits)
				r.Post("/cordon", workerHandlers.Cordon)
				r.Delete("/cordon", workerHandlers.Uncordon)
				r.Patch("/tags", workerHandlers.SetTags)
				r.Delete("/tags", workerHandlers.ClearTags)
			})
		})

		r.Route("/settings", func(r chi.Router) {
			r.With(RequireClusterPermission(opts.SettingsSvc, service.PermSettingsView)).Get("/", settingsAPIHandlers.Get)
			r.With(RequireClusterPermission(opts.SettingsSvc, service.PermSettingsEdit)).Patch("/", settingsAPIHandlers.Update)
		})

		r.Route("/webhooks", func(r chi.Router) {
			r.Use(RequireClusterPermission(opts.SettingsSvc, service.PermWebhooksManage))
			r.Get("/", webhookHandlers.List)
			r.Post("/", webhookHandlers.Create)
			r.Get("/{webhookId}", webhookHandlers.Get)
			r.Patch("/{webhookId}", webhookHandlers.Update)
			r.Delete("/{webhookId}", webhookHandlers.Delete)
			r.Post("/{webhookId}/test", webhookHandlers.Test)
			r.Get("/{webhookId}/deliveries", webhookHandlers.Deliveries)
		})

		r.Route("/tokens", func(r chi.Router) {
			r.Use(RequireClusterPermission(opts.SettingsSvc, service.PermTokensManage))
			r.Get("/", authHandlers.ListTokens)
			r.Post("/", authHandlers.CreateToken)
			r.Delete("/{tokenId}", authHandlers.DeleteToken)
		})

		r.Route("/worker-tokens", func(r chi.Router) {
			r.Use(RequireClusterPermission(opts.SettingsSvc, service.PermTokensManage))
			r.Get("/", authHandlers.ListWorkerTokens)
			r.Post("/", authHandlers.CreateWorkerToken)
			r.Delete("/{tokenId}", authHandlers.DeleteWorkerToken)
		})
	})

	return r
}

// securityHeaders sets standard protective headers on every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

func jsonContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		next.ServeHTTP(w, r)
	})
}
