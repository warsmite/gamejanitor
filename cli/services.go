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
	"github.com/warsmite/gamejanitor/steam"
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
	ReadyWatcher    *status.ReadyWatcher
	ConsoleSvc      *gameserver.ConsoleService
	FileSvc         *gameserver.FileService
	BackupSvc       *backup.BackupService
	Scheduler       *schedule.Scheduler
	ScheduleSvc     *schedule.ScheduleService
	AuthSvc         *auth.AuthService
	StatusMgr       *status.StatusManager
	StatusSub       *status.StatusSubscriber
	EventHistorySvc *event.EventHistoryService
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
	statsPoller := status.NewStatsPoller(db, dispatcher, broadcaster, logger)
	readyWatcher := status.NewReadyWatcher(db, broadcaster, gameStore, logger)
	gameserverSvc.SetReadyWatcher(readyWatcher)
	consoleSvc := gameserver.NewConsoleService(db, dispatcher, gameStore, logger)
	fileSvc := gameserver.NewFileService(db, dispatcher, logger)

	// Activity tracking
	activityTracker := gameserver.NewActivityTracker(db, logger)
	gameserverSvc.SetActivityTracker(activityTracker)

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
	statusMgr := status.NewStatusManager(db, broadcaster, querySvc, statsPoller, readyWatcher, dispatcher, registry, gameserverSvc.Start, logger)
	statusSub := status.NewStatusSubscriber(db, broadcaster, querySvc, statsPoller, logger)
	eventHistorySvc := event.NewEventHistoryService(db)
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

	// Steam depot downloader — provides authenticated game file downloads
	// for games that can't use anonymous SteamCMD.
	// Credentials are read live from settings so `steam login` works without restart.
	steamCreds := &steamCredentialAdapter{settings: settingsSvc}
	steamSvc := steam.NewService(logger, cfg.DataDir, steamCreds)
	gameserverSvc.SetSteamDepot(steamSvc)

	return &Services{
		Broadcaster:     broadcaster,
		SettingsSvc:     settingsSvc,
		GameserverSvc:   gameserverSvc,
		QuerySvc:        querySvc,
		StatsPoller:     statsPoller,
		ReadyWatcher:    readyWatcher,
		ConsoleSvc:      consoleSvc,
		FileSvc:         fileSvc,
		BackupSvc:       backupSvc,
		Scheduler:       scheduler,
		ScheduleSvc:     scheduleSvc,
		AuthSvc:         authSvc,
		StatusMgr:       statusMgr,
		StatusSub:       statusSub,
		EventHistorySvc: eventHistorySvc,
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

// steamCredentialAdapter bridges the settings service to the steam.CredentialProvider interface.
// Reads credentials live from the DB so `gamejanitor steam login` works without a server restart.
type steamCredentialAdapter struct {
	settings *settings.SettingsService
}

func (a *steamCredentialAdapter) SteamAccountName() string {
	return a.settings.GetString(settings.SettingSteamAccountName)
}

func (a *steamCredentialAdapter) SteamRefreshToken() string {
	return a.settings.GetString(settings.SettingSteamRefreshToken)
}
