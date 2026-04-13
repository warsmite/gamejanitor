package cli

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/backup"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/file"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/controller/schedule"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/util/netutil"
	"github.com/warsmite/gamejanitor/store"
)

// Services holds all wired services. Exported so testutil can use the same wiring.
type Services struct {
	Broadcaster     *event.EventBus
	SettingsSvc     *settings.SettingsService
	Manager         *gameserver.Manager
	QuerySvc        *cluster.QueryService
	StatsPoller     *cluster.StatsPoller
	ConsoleSvc      *gameserver.ConsoleService
	FileSvc         *file.Service
	BackupSvc       *backup.BackupService
	Scheduler       *schedule.Scheduler
	ScheduleSvc     *schedule.ScheduleService
	AuthSvc         *auth.AuthService
	EventHistorySvc *event.EventHistoryService
	EventPersister  *event.EventPersister
	WebhookWorker   *webhook.WebhookWorker
	WebhookSvc      *webhook.WebhookEndpointService
	WorkerNodeSvc   *cluster.WorkerNodeService
	ModSvc          *mod.ModService
	BackupStorage   backup.Storage
}

// InitServicesOpts configures optional overrides for service initialization.
type InitServicesOpts struct {
	// BackupStorage overrides the backup storage backend. Nil uses config-based detection.
	BackupStorage backup.Storage
	// SkipConfigApply skips applying config file settings to DB. Used in tests.
	SkipConfigApply bool
}

// InitServices wires all services together. This is the single composition root
// used by both production (cli/serve.go) and tests (testutil/services.go).
func InitServices(database *sql.DB, dispatcher *cluster.Dispatcher, registry *cluster.Registry, gameStore *games.GameStore, cfg config.Config, logger *slog.Logger, opts *InitServicesOpts) (*Services, error) {
	if opts == nil {
		opts = &InitServicesOpts{}
	}

	broadcaster := event.NewEventBus()
	db := store.New(database)

	settingsSvc := settings.NewSettingsServiceWithMode(db, logger, cfg.Mode)

	if !opts.SkipConfigApply {
		settingsSvc.ApplyConfig(cfg.Settings)
	}

	// Placement service (shared between gameserver creation and lifecycle)
	placementSvc := cluster.NewPlacementService(db, dispatcher, settingsSvc, logger)

	// Backup storage
	var backupStorage backup.Storage
	if opts.BackupStorage != nil {
		backupStorage = opts.BackupStorage
	} else {
		var err error
		backupStorage, err = initBackupStorage(cfg, logger)
		if err != nil {
			return nil, err
		}
	}

	// Gameserver manager — the core aggregate holder
	manager := gameserver.NewManager(db, dispatcher, registry, broadcaster, settingsSvc, gameStore, placementSvc, backupStorage, cfg.DataDir, cfg.SFTPPort, logger)

	querySvc := cluster.NewQueryService(db, broadcaster, gameStore, logger)
	statsPoller := cluster.NewStatsPoller(db, dispatcher, broadcaster, db.GameserverStatsStore, logger)
	statsPoller.SetPlayerCountFn(func(gsID string) int {
		if q := querySvc.GetQueryData(gsID); q != nil {
			return q.PlayersOnline
		}
		return 0
	})
	manager.SetPollers(statsPoller, querySvc)
	consoleSvc := gameserver.NewConsoleService(db, dispatcher, gameStore, logger)
	fileSvc := file.NewService(db, dispatcher, logger)

	backupSvc := backup.NewBackupService(db, dispatcher, manager, gameStore, backupStorage, settingsSvc, broadcaster, logger)
	scheduler := schedule.NewScheduler(db, backupSvc, manager, consoleSvc, broadcaster, logger)
	scheduleSvc := schedule.NewScheduleService(db, scheduler, broadcaster, logger)
	authSvc := auth.NewAuthService(db, logger)
	eventHistorySvc := event.NewEventHistoryService(db)
	eventPersister := event.NewEventPersister(db, broadcaster, logger)
	webhookWorker := webhook.NewWebhookWorker(db, db, broadcaster, logger)
	webhookWorker.ValidateURL = func(rawURL string) error {
		if !settingsSvc.GetBool(settings.SettingRestrictDownloadURLs) {
			return nil
		}
		return netutil.ValidateWebhookURL(rawURL)
	}
	webhookSvc := webhook.NewWebhookEndpointService(db, logger)
	workerNodeSvc := cluster.NewWorkerNodeService(db, registry, broadcaster, logger)
	optionsRegistry := games.NewOptionsRegistry(logger)
	modSvc := mod.NewModService(db, fileSvc, gameStore, optionsRegistry, broadcaster, logger)

	// Register mod catalogs
	modSvc.RegisterCatalog("modrinth", mod.NewModrinthCatalog(logger.With("catalog", "modrinth")))
	modSvc.RegisterCatalog("umod", mod.NewUmodCatalog(logger.With("catalog", "umod")))
	modSvc.RegisterCatalog("workshop", mod.NewWorkshopCatalog(settingsSvc, logger.With("catalog", "workshop")))
	modSvc.ValidateCatalogs()
	modSvc.SetURLValidator(func(rawURL string) error {
		if !settingsSvc.GetBool(settings.SettingRestrictDownloadURLs) {
			return nil
		}
		return netutil.ValidateExternalURL(rawURL)
	})

	// Set mod reconciler on manager so lifecycle start calls it
	manager.SetModReconciler(modSvc)

	// Register worker lifecycle callbacks
	registry.SetCallbacks(manager.OnWorkerOnline, manager.OnWorkerOffline)

	return &Services{
		Broadcaster:     broadcaster,
		SettingsSvc:     settingsSvc,
		Manager:         manager,
		QuerySvc:        querySvc,
		StatsPoller:     statsPoller,
		ConsoleSvc:      consoleSvc,
		FileSvc:         fileSvc,
		BackupSvc:       backupSvc,
		Scheduler:       scheduler,
		ScheduleSvc:     scheduleSvc,
		AuthSvc:         authSvc,
		EventHistorySvc: eventHistorySvc,
		EventPersister:  eventPersister,
		WebhookWorker:   webhookWorker,
		WebhookSvc:      webhookSvc,
		WorkerNodeSvc:   workerNodeSvc,
		ModSvc:          modSvc,
		BackupStorage:   backupStorage,
	}, nil
}

func initBackupStorage(cfg config.Config, logger *slog.Logger) (backup.Storage, error) {
	bs := cfg.BackupStore
	if bs == nil || bs.Type == "" || bs.Type == "local" {
		logger.Info("backup store: local", "path", cfg.DataDir)
		return backup.NewLocalStorage(cfg.DataDir), nil
	}

	if bs.Type == "s3" {
		s3Storage, err := backup.NewS3Storage(bs, logger)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize backup store: %w", err)
		}
		return s3Storage, nil
	}

	return nil, fmt.Errorf("unknown backup_store type: %q (must be \"local\" or \"s3\")", bs.Type)
}
