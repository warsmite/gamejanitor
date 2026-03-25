package testutil

import (
	"database/sql"
	"testing"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/service"
	"github.com/warsmite/gamejanitor/worker"
)

// ServiceBundle holds all services wired together for testing.
type ServiceBundle struct {
	DB            *sql.DB
	GameStore     *games.GameStore
	Registry      *worker.Registry
	Dispatcher    *worker.Dispatcher
	Broadcaster   *service.EventBus
	SettingsSvc   *service.SettingsService
	GameserverSvc *service.GameserverService
	QuerySvc      *service.QueryService
	StatsPoller   *service.StatsPoller
	ReadyWatcher  *service.ReadyWatcher
	ConsoleSvc    *service.ConsoleService
	FileSvc       *service.FileService
	BackupSvc     *service.BackupService
	Scheduler     *service.Scheduler
	ScheduleSvc   *service.ScheduleService
	AuthSvc       *service.AuthService
	ModSvc        *service.ModService
	BackupStore   service.BackupStore
	StatusSub     *service.StatusSubscriber
	EventStore    *service.EventStoreSubscriber
}

// NewTestServices wires all services with a real in-memory DB, fake workers, and real event bus.
// Mirrors the production initServices in cli/serve.go.
func NewTestServices(t *testing.T) *ServiceBundle {
	t.Helper()

	db := NewTestDB(t)
	log := TestLogger()
	gameStore := NewTestGameStore(t)

	registry := worker.NewRegistry(log)
	dispatcher := worker.NewDispatcher(registry, db, log)
	broadcaster := service.NewEventBus()
	settingsSvc := service.NewSettingsService(db, log)

	dataDir := t.TempDir()
	backupStore := service.NewLocalStore(dataDir)

	gameserverSvc := service.NewGameserverService(db, dispatcher, broadcaster, settingsSvc, gameStore, dataDir, log)
	querySvc := service.NewQueryService(db, broadcaster, gameStore, log)
	statsPoller := service.NewStatsPoller(db, dispatcher, broadcaster, log)
	readyWatcher := service.NewReadyWatcher(db, broadcaster, gameStore, log)
	readyWatcher.SetQueryService(querySvc)
	readyWatcher.SetStatsPoller(statsPoller)
	gameserverSvc.SetReadyWatcher(readyWatcher)
	gameserverSvc.SetBackupStore(backupStore)

	consoleSvc := service.NewConsoleService(db, dispatcher, gameStore, log)
	fileSvc := service.NewFileService(db, dispatcher, log)
	backupSvc := service.NewBackupService(db, dispatcher, gameserverSvc, gameStore, backupStore, settingsSvc, broadcaster, log)
	scheduler := service.NewScheduler(db, backupSvc, gameserverSvc, consoleSvc, broadcaster, log)
	scheduleSvc := service.NewScheduleService(db, scheduler, broadcaster, log)
	authSvc := service.NewAuthService(db, log)

	optionsRegistry := games.NewOptionsRegistry(log)
	modSvc := service.NewModService(db, fileSvc, gameStore, settingsSvc, optionsRegistry, broadcaster, log)

	svc := &ServiceBundle{
		DB:            db,
		GameStore:     gameStore,
		Registry:      registry,
		Dispatcher:    dispatcher,
		Broadcaster:   broadcaster,
		SettingsSvc:   settingsSvc,
		GameserverSvc: gameserverSvc,
		QuerySvc:      querySvc,
		StatsPoller:   statsPoller,
		ReadyWatcher:  readyWatcher,
		ConsoleSvc:    consoleSvc,
		FileSvc:       fileSvc,
		BackupSvc:     backupSvc,
		Scheduler:     scheduler,
		ScheduleSvc:   scheduleSvc,
		AuthSvc:       authSvc,
		ModSvc:        modSvc,
		BackupStore:   backupStore,
	}

	t.Cleanup(func() {
		readyWatcher.StopAll()
		querySvc.StopAll()
	})

	return svc
}

// NewTestServicesWithSubscribers is like NewTestServices but also starts the async
// event subscribers (StatusSubscriber, EventStoreSubscriber). Use this for tests that
// need to verify status derivation from lifecycle events or event persistence to the DB.
// Subscribers are stopped on test cleanup.
func NewTestServicesWithSubscribers(t *testing.T) *ServiceBundle {
	t.Helper()
	svc := NewTestServices(t)
	log := TestLogger()

	statusSub := service.NewStatusSubscriber(svc.DB, svc.Broadcaster, log)
	eventStore := service.NewEventStoreSubscriber(svc.DB, svc.Broadcaster, log)

	ctx := TestContext()
	statusSub.Start(ctx)
	eventStore.Start(ctx)

	t.Cleanup(func() {
		// Stop ReadyWatcher first — its goroutines hold references to
		// the worker and DB. If we close those first, the watcher panics.
		svc.ReadyWatcher.StopAll()
		svc.QuerySvc.StopAll()
		statusSub.Stop()
		eventStore.Stop()
	})

	svc.StatusSub = statusSub
	svc.EventStore = eventStore

	return svc
}

// RegisterFakeWorker creates a FakeWorker, registers it in the registry, and returns it.
// The worker is registered with the given nodeID and also persisted as a worker_node in the DB.
func RegisterFakeWorker(t *testing.T, svc *ServiceBundle, nodeID string, opts ...FakeWorkerOption) *FakeWorker {
	t.Helper()

	fw := NewFakeWorker(t)
	fw.ReadyPattern = "Server is ready"

	cfg := fakeWorkerConfig{
		maxMemoryMB:  16384,
		maxCPU:       8.0,
		maxStorageMB: 102400,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	tags := cfg.tags
	if tags == nil {
		tags = models.Labels{}
	}

	// Persist the worker node record in the DB so placement queries find it
	_, err := svc.DB.Exec(`INSERT INTO worker_nodes (id, max_memory_mb, max_cpu, max_storage_mb, tags) VALUES (?, ?, ?, ?, ?)`,
		nodeID, cfg.maxMemoryMB, cfg.maxCPU, cfg.maxStorageMB, tags)
	if err != nil {
		t.Fatalf("inserting worker node: %v", err)
	}

	info := worker.WorkerInfo{ID: nodeID}
	svc.Registry.Register(nodeID, fw, info)

	t.Cleanup(func() {
		svc.Registry.Unregister(nodeID)
	})

	return fw
}

type fakeWorkerConfig struct {
	maxMemoryMB  int
	maxCPU       float64
	maxStorageMB int
	tags         models.Labels
}

type FakeWorkerOption func(*fakeWorkerConfig)

func WithMaxMemoryMB(mb int) FakeWorkerOption {
	return func(c *fakeWorkerConfig) { c.maxMemoryMB = mb }
}

func WithMaxCPU(cpu float64) FakeWorkerOption {
	return func(c *fakeWorkerConfig) { c.maxCPU = cpu }
}

func WithMaxStorageMB(mb int) FakeWorkerOption {
	return func(c *fakeWorkerConfig) { c.maxStorageMB = mb }
}

func WithTags(tags models.Labels) FakeWorkerOption {
	return func(c *fakeWorkerConfig) { c.tags = tags }
}

// MustCreateAdminToken creates an admin API token and returns the raw token string.
func MustCreateAdminToken(t *testing.T, svc *ServiceBundle) string {
	t.Helper()
	raw, _, err := svc.AuthSvc.CreateAdminToken("test-admin")
	if err != nil {
		t.Fatalf("creating admin token: %v", err)
	}
	return raw
}

// MustCreateCustomToken creates a custom API token with the given permissions and optional gameserver ID scoping.
func MustCreateCustomToken(t *testing.T, svc *ServiceBundle, perms []string, gameserverIDs []string) string {
	t.Helper()
	raw, _, err := svc.AuthSvc.CreateCustomToken("test-custom", gameserverIDs, perms, nil)
	if err != nil {
		t.Fatalf("creating custom token: %v", err)
	}
	return raw
}
