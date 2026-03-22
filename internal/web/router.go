package web

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/warsmite/gamejanitor/internal/games"
	"github.com/warsmite/gamejanitor/internal/netinfo"
	"github.com/warsmite/gamejanitor/internal/service"
	"github.com/warsmite/gamejanitor/internal/web/handlers"
	"github.com/warsmite/gamejanitor/internal/web/static"
	"github.com/warsmite/gamejanitor/internal/worker"
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
	broadcaster *service.EventBus,
	netInfo *netinfo.Info,
	registry *worker.Registry,
	db *sql.DB,
	logPath string,
	dataDir string,
	bindAddress string,
	port int,
	sftpPort int,
	role string,
	log *slog.Logger,
) (http.Handler, error) {
	renderer, err := handlers.NewRenderer(netInfo, settingsSvc, bindAddress, port, sftpPort, role)
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
	r.Use(securityHeaders)

	rateLimitStore := NewRateLimitStore(settingsSvc, log)
	r.Use(rateLimitStore.PerIPMiddleware())

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
	eventHandlers := handlers.NewEventHandlers(broadcaster, db, log)
	scheduleHandlers := handlers.NewScheduleHandlers(scheduleSvc, log)
	backupHandlers := handlers.NewBackupHandlers(backupSvc, log)
	fileHandlers := handlers.NewFileHandlers(fileSvc, log)
	logHandlers := handlers.NewLogHandlers(logPath, log)
	statusHandlers := handlers.NewStatusHandlers(gameserverSvc, querySvc, log)
	authHandlers := handlers.NewAuthHandlers(authSvc, log)
	workerNodeSvc := service.NewWorkerNodeService(db, log)
	workerHandlers := handlers.NewWorkerHandlers(registry, workerNodeSvc, gameserverSvc, log)
	settingsAPIHandlers := handlers.NewSettingsAPIHandlers(settingsSvc, log)
	webhookHandlers := handlers.NewWebhookHandlers(db, log)
	requireAdmin := RequireAdmin(settingsSvc)
	requireAccess := RequireGameserverAccess(settingsSvc)
	requireStart := RequirePermission(settingsSvc, service.PermGameserverStart)
	requireStop := RequirePermission(settingsSvc, service.PermGameserverStop)
	requireRestart := RequirePermission(settingsSvc, service.PermGameserverRestart)
	requireLogs := RequirePermission(settingsSvc, service.PermGameserverLogs)
	requireCommands := RequirePermission(settingsSvc, service.PermGameserverCommand)
	requireFilesRead := RequirePermission(settingsSvc, service.PermGameserverFilesRead)
	requireFilesWrite := RequirePermission(settingsSvc, service.PermGameserverFilesWrite)
	requireBackupCreate := RequirePermission(settingsSvc, service.PermBackupCreate)
	requireBackupDelete := RequirePermission(settingsSvc, service.PermBackupDelete)
	requireBackupRestore := RequirePermission(settingsSvc, service.PermBackupRestore)
	requireBackupDownload := RequirePermission(settingsSvc, service.PermBackupDownload)
	requireScheduleCreate := RequirePermission(settingsSvc, service.PermScheduleCreate)
	requireScheduleUpdate := RequirePermission(settingsSvc, service.PermScheduleUpdate)
	requireScheduleDelete := RequirePermission(settingsSvc, service.PermScheduleDelete)
	requireConfigure := RequirePermission(settingsSvc, service.PermGameserverEditEnv)
	requireDelete := RequirePermission(settingsSvc, service.PermGameserverDelete)


	r.Route("/api", func(r chi.Router) {
		r.Use(jsonContentType)
		r.Use(authMiddleware)
		r.Use(rateLimitStore.PerTokenMiddleware())


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
			r.Use(RequireClusterPermission(settingsSvc, service.PermNodesManage))
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
			r.With(RequireClusterPermission(settingsSvc, service.PermSettingsView)).Get("/", settingsAPIHandlers.Get)
			r.With(RequireClusterPermission(settingsSvc, service.PermSettingsEdit)).Patch("/", settingsAPIHandlers.Update)
		})

		r.Route("/webhooks", func(r chi.Router) {
			r.Use(RequireClusterPermission(settingsSvc, service.PermWebhooksManage))
			r.Get("/", webhookHandlers.List)
			r.Post("/", webhookHandlers.Create)
			r.Get("/{webhookId}", webhookHandlers.Get)
			r.Patch("/{webhookId}", webhookHandlers.Update)
			r.Delete("/{webhookId}", webhookHandlers.Delete)
			r.Post("/{webhookId}/test", webhookHandlers.Test)
			r.Get("/{webhookId}/deliveries", webhookHandlers.Deliveries)
		})

		r.Route("/tokens", func(r chi.Router) {
			r.Use(RequireClusterPermission(settingsSvc, service.PermTokensManage))
			r.Get("/", authHandlers.ListTokens)
			r.Post("/", authHandlers.CreateToken)
			r.Delete("/{tokenId}", authHandlers.DeleteToken)
		})

		r.Route("/worker-tokens", func(r chi.Router) {
			r.Use(RequireClusterPermission(settingsSvc, service.PermTokensManage))
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
		r.Use(rateLimitStore.LoginRateLimitMiddleware())
		r.Get("/login", pageAuth.LoginPage)
		r.Post("/login", pageAuth.Login)
		r.Post("/logout", pageAuth.Logout)
	})

	// Page handlers (HTML)
	pageDashboard := handlers.NewPageDashboardHandlers(gameStore, gameserverSvc, querySvc, settingsSvc, registry, renderer, log)
	pageGames := handlers.NewPageGameHandlers(gameStore, gameserverSvc, renderer, log)
	pageGameservers := handlers.NewPageGameserverHandlers(gameStore, gameserverSvc, scheduleSvc, querySvc, settingsSvc, registry, renderer, db, log)
	pageSettings := handlers.NewPageSettingsHandlers(settingsSvc, workerNodeSvc, authSvc, db, registry, renderer, dataDir, log)
	pageActions := handlers.NewPageActionHandlers(gameStore, gameserverSvc, renderer, log)
	pageConsole := handlers.NewPageConsoleHandlers(consoleSvc, gameStore, gameserverSvc, renderer, log)
	pageFiles := handlers.NewPageFileHandlers(fileSvc, gameStore, gameserverSvc, renderer, log)
	pageSchedules := handlers.NewPageScheduleHandlers(scheduleSvc, gameStore, gameserverSvc, renderer, log)
	pageBackups := handlers.NewPageBackupHandlers(backupSvc, gameStore, gameserverSvc, renderer, log)

	r.Group(func(r chi.Router) {
		r.Use(plaintextMiddleware)
		r.Use(csrfMiddleware)
		r.Use(authMiddleware)
		r.Use(rateLimitStore.PerTokenMiddleware())


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
			r.Post("/rate-limit/enable", pageSettings.SetRateLimitEnabled(true))
			r.Post("/rate-limit/disable", pageSettings.SetRateLimitEnabled(false))
			r.Post("/rate-limit/per-ip", pageSettings.SaveRateLimitPerIP)
			r.Post("/rate-limit/per-token", pageSettings.SaveRateLimitPerToken)
			r.Post("/rate-limit/login", pageSettings.SaveRateLimitLogin)
			r.Post("/trust-proxy-headers/enable", pageSettings.SetTrustProxyHeaders(true))
			r.Post("/trust-proxy-headers/disable", pageSettings.SetTrustProxyHeaders(false))
			r.Post("/workers/{workerID}/port-range", pageSettings.SaveWorkerPortRange)
			r.Delete("/workers/{workerID}/port-range", pageSettings.ClearWorkerPortRange)
			r.Post("/workers/{workerID}/limits", pageSettings.SaveWorkerLimits)
			r.Delete("/workers/{workerID}/limits", pageSettings.ClearWorkerLimits)
			r.Post("/workers/{workerID}/cordon", pageSettings.CordonWorker)
			r.Delete("/workers/{workerID}/cordon", pageSettings.UncordonWorker)
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
				r.With(requireConfigure).Get("/edit", pageGameservers.Edit)
				r.With(requireConfigure).Patch("/", pageGameservers.Update)
				r.With(requireDelete).Delete("/", pageGameservers.Delete)

				// Lifecycle actions
				r.With(requireStart).Post("/start", pageActions.Start)
				r.With(requireStop).Post("/stop", pageActions.Stop)
				r.With(requireRestart).Post("/restart", pageActions.Restart)
				r.With(requireConfigure).Post("/update-game", pageActions.UpdateGame)
				r.With(requireConfigure).Post("/reinstall", pageActions.Reinstall)
				r.With(requireAdmin).Post("/regenerate-sftp-password", pageGameservers.RegenerateSFTPPassword)

				// Console
				r.With(requireLogs).Get("/console", pageConsole.Console)
				r.With(requireLogs).Get("/console/stream", pageConsole.LogStream)
				r.With(requireLogs).Get("/console/sessions", pageConsole.Sessions)
				r.With(requireCommands).Post("/console/command", pageConsole.SendCommand)

				// Files
				r.With(requireFilesRead).Get("/files", pageFiles.List)
				r.With(requireFilesRead).Get("/files/list", pageFiles.ListJSON)
				r.With(requireFilesRead).Get("/files/content", pageFiles.ReadFile)
				r.With(requireFilesWrite).Put("/files/content", pageFiles.WriteFile)
				r.With(requireFilesWrite).Delete("/files/entry", pageFiles.DeletePath)
				r.With(requireFilesWrite).Post("/files/mkdir", pageFiles.CreateDirectory)
				r.With(requireFilesRead).Get("/files/download", pageFiles.DownloadFile)
				r.With(requireFilesWrite).Post("/files/upload", pageFiles.UploadFile)
				r.With(requireFilesWrite).Post("/files/rename", pageFiles.RenamePath)

				// Schedules
				r.With(requireScheduleCreate).Get("/schedules", pageSchedules.List)
				r.With(requireScheduleCreate).Post("/schedules", pageSchedules.Create)
				r.With(requireScheduleUpdate).Patch("/schedules/{scheduleId}", pageSchedules.Update)
				r.With(requireScheduleDelete).Delete("/schedules/{scheduleId}", pageSchedules.Delete)
				r.With(requireScheduleUpdate).Post("/schedules/{scheduleId}/toggle", pageSchedules.Toggle)

				// Backups
				r.With(requireBackupCreate).Get("/backups", pageBackups.List)
				r.With(requireBackupCreate).Post("/backups", pageBackups.Create)
				r.With(requireBackupDownload).Get("/backups/{backupId}/download", pageBackups.Download)
				r.With(requireBackupRestore).Post("/backups/{backupId}/restore", pageBackups.Restore)
				r.With(requireBackupDelete).Delete("/backups/{backupId}", pageBackups.Delete)
			})
		})
	})

	return r, nil
}

// securityHeaders sets standard protective headers on every response.
// script-src/style-src use 'unsafe-inline' + 'unsafe-eval' because Alpine.js
// evaluates expressions via new Function() and templates use inline scripts/styles.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		next.ServeHTTP(w, r)
	})
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
