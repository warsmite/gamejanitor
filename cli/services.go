package cli

import (
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/backup"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/schedule"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/controller/webhook"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/pkg/netutil"
	"github.com/warsmite/gamejanitor/store"
)

// Services holds all wired services. Exported so testutil can use the same wiring.
type Services struct {
	Broadcaster     *controller.EventBus
	SettingsSvc     *settings.SettingsService
	GameserverSvc   *gameserver.GameserverService
	QuerySvc        *status.QueryService
	StatsPoller     *status.StatsPoller
	ConsoleSvc      *gameserver.ConsoleService
	FileSvc         *gameserver.FileService
	BackupSvc       *backup.BackupService
	Scheduler       *schedule.Scheduler
	ScheduleSvc     *schedule.ScheduleService
	AuthSvc         *auth.AuthService
	StatusMgr       *status.StatusManager
	StatusSub       *status.StatusSubscriber
	EventHistorySvc *event.EventHistoryService
	EventPersister  *event.EventPersister
	WebhookWorker   *webhook.WebhookWorker
	WebhookSvc      *webhook.WebhookEndpointService
	WorkerNodeSvc   *orchestrator.WorkerNodeService
	ModSvc          *mod.ModService
	BackupStorage   backup.Storage
	ActivityTracker *gameserver.ActivityTracker
}

// InitServicesOpts configures optional overrides for service initialization.
type InitServicesOpts struct {
	// PortProbe overrides the port availability check. Tests set this to always return true.
	PortProbe func(int) bool
	// BackupStorage overrides the backup storage backend. Nil uses config-based detection.
	BackupStorage backup.Storage
	// SkipConfigApply skips applying config file settings to DB. Used in tests.
	SkipConfigApply bool
}

// InitServices wires all services together. This is the single composition root
// used by both production (cli/serve.go) and tests (testutil/services.go).
func InitServices(database *sql.DB, dispatcher *orchestrator.Dispatcher, registry *orchestrator.Registry, gameStore *games.GameStore, cfg config.Config, logger *slog.Logger, opts *InitServicesOpts) (*Services, error) {
	if opts == nil {
		opts = &InitServicesOpts{}
	}

	broadcaster := controller.NewEventBus()
	db := store.New(database)

	settingsSvc := settings.NewSettingsServiceWithMode(db, logger, cfg.Mode)

	if !opts.SkipConfigApply {
		settingsSvc.ApplyConfig(cfg.Settings)
	}

	gameserverSvc := gameserver.NewGameserverService(db, dispatcher, broadcaster, settingsSvc, gameStore, cfg.DataDir, logger)
	querySvc := status.NewQueryService(db, broadcaster, gameStore, logger)
	statsPoller := status.NewStatsPoller(db, dispatcher, broadcaster, db.GameserverStatsStore, logger)
	statsPoller.SetPlayerCountFn(func(gsID string) int {
		if q := querySvc.GetQueryData(gsID); q != nil {
			return q.PlayersOnline
		}
		return 0
	})
	// ReadyWatcher is now worker-side — no controller-side watcher needed
	consoleSvc := gameserver.NewConsoleService(db, dispatcher, gameStore, logger)
	fileSvc := gameserver.NewFileService(db, dispatcher, logger)

	// Activity tracking
	activityTracker := gameserver.NewActivityTracker(db, logger)
	gameserverSvc.SetActivityTracker(activityTracker)

	// Operation tracking (transient in-memory state for active operations)
	operationTracker := gameserver.NewOperationTracker(broadcaster, logger)
	gameserverSvc.SetOperationTracker(operationTracker)

	// Port probe override (tests skip host port checking)
	if opts.PortProbe != nil {
		gameserverSvc.SetPortProbe(opts.PortProbe)
	}

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

	gameserverSvc.SetBackupStore(backupStorage)
	backupSvc := backup.NewBackupService(db, dispatcher, gameserverSvc, gameStore, backupStorage, settingsSvc, broadcaster, logger)
	backupSvc.SetActivityTracker(activityTracker)
	scheduler := schedule.NewScheduler(db, backupSvc, gameserverSvc, consoleSvc, broadcaster, logger)
	scheduleSvc := schedule.NewScheduleService(db, scheduler, broadcaster, logger)
	authSvc := auth.NewAuthService(db, logger)
	statusMgr := status.NewStatusManager(db, broadcaster, querySvc, statsPoller, dispatcher, registry, gameserverSvc.RestartAfterCrash, logger)
	gameserverSvc.SetStatusProvider(statusMgr)
	statusSub := status.NewStatusSubscriber(db, broadcaster, querySvc, statsPoller, logger)
	statusSub.SetOperationClearer(operationTracker)
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
	workerNodeSvc := orchestrator.NewWorkerNodeService(db, registry, broadcaster, logger)
	optionsRegistry := games.NewOptionsRegistry(logger)
	modSvc := mod.NewModService(db, fileSvc, gameStore, optionsRegistry, broadcaster, logger)

	// Register mod catalogs — stateless query engines, game-specific config
	// (uMod category, Workshop appID) comes from game YAML via CatalogFilters.Extra
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
	gameserverSvc.SetModReconciler(modSvc)

	return &Services{
		Broadcaster:     broadcaster,
		SettingsSvc:     settingsSvc,
		GameserverSvc:   gameserverSvc,
		QuerySvc:        querySvc,
		StatsPoller:     statsPoller,
		ConsoleSvc:      consoleSvc,
		FileSvc:         fileSvc,
		BackupSvc:       backupSvc,
		Scheduler:       scheduler,
		ScheduleSvc:     scheduleSvc,
		AuthSvc:         authSvc,
		StatusMgr:       statusMgr,
		StatusSub:       statusSub,
		EventHistorySvc: eventHistorySvc,
		EventPersister:  eventPersister,
		WebhookWorker:   webhookWorker,
		WebhookSvc:      webhookSvc,
		WorkerNodeSvc:   workerNodeSvc,
		ModSvc:          modSvc,
		BackupStorage:   backupStorage,
		ActivityTracker: activityTracker,
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

