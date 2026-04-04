package testutil

import (
	"database/sql"
	"testing"

	"github.com/warsmite/gamejanitor/cli"
	"github.com/warsmite/gamejanitor/config"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/backup"
	"github.com/warsmite/gamejanitor/controller/gameserver"
	"github.com/warsmite/gamejanitor/controller/mod"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/schedule"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/controller/status"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/stretchr/testify/require"
)

// ServiceBundle holds all services wired together for testing.
// Uses the same InitServices as production to prevent wiring drift.
type ServiceBundle struct {
	DB            *sql.DB
	GameStore     *games.GameStore
	Registry      *orchestrator.Registry
	Dispatcher    *orchestrator.Dispatcher
	Broadcaster   *controller.EventBus
	SettingsSvc   *settings.SettingsService
	GameserverSvc *gameserver.GameserverService
	QuerySvc      *status.QueryService
	StatsPoller   *status.StatsPoller
	ConsoleSvc    *gameserver.ConsoleService
	FileSvc       *gameserver.FileService
	BackupSvc     *backup.BackupService
	Scheduler     *schedule.Scheduler
	ScheduleSvc   *schedule.ScheduleService
	AuthSvc       *auth.AuthService
	ModSvc        *mod.ModService
	BackupStorage backup.Storage
	StatusSub     *status.StatusSubscriber
	StatusMgr     *status.StatusManager
}

// NewTestServices wires all services with a real in-memory DB, fake workers, and real event bus.
// Uses the SAME InitServices as production — no wiring drift possible.
func NewTestServices(t *testing.T) *ServiceBundle {
	t.Helper()

	db := NewTestDB(t)
	log := TestLogger()
	gameStore := NewTestGameStore(t)
	s := store.New(db)

	registry := orchestrator.NewRegistry(s, log)
	dispatcher := orchestrator.NewDispatcher(registry, s, log)

	dataDir := t.TempDir()
	cfg := config.DefaultConfig()
	cfg.DataDir = dataDir

	svcs, err := cli.InitServices(db, dispatcher, registry, gameStore, cfg, log, &cli.InitServicesOpts{
		BackupStorage:   backup.NewLocalStorage(dataDir),
		SkipConfigApply: true,
	})
	require.NoError(t, err)

	svc := &ServiceBundle{
		DB:            db,
		GameStore:     gameStore,
		Registry:      registry,
		Dispatcher:    dispatcher,
		Broadcaster:   svcs.Broadcaster,
		SettingsSvc:   svcs.SettingsSvc,
		GameserverSvc: svcs.GameserverSvc,
		QuerySvc:      svcs.QuerySvc,
		StatsPoller:   svcs.StatsPoller,
		ConsoleSvc:    svcs.ConsoleSvc,
		FileSvc:       svcs.FileSvc,
		BackupSvc:     svcs.BackupSvc,
		Scheduler:     svcs.Scheduler,
		ScheduleSvc:   svcs.ScheduleSvc,
		AuthSvc:       svcs.AuthSvc,
		ModSvc:        svcs.ModSvc,
		BackupStorage: svcs.BackupStorage,
		StatusSub:     svcs.StatusSub,
		StatusMgr:     svcs.StatusMgr,
	}

	t.Cleanup(func() {
		svcs.QuerySvc.StopAll()
	})

	return svc
}

// NewTestServicesWithSubscribers is like NewTestServices but also starts the async
// event subscribers (StatusSubscriber). Use this for tests that need to verify
// status derivation from lifecycle events.
// Subscribers are stopped on test cleanup.
func NewTestServicesWithSubscribers(t *testing.T) *ServiceBundle {
	t.Helper()
	svc := NewTestServices(t)

	svc.GameserverSvc.SetStatusProvider(svc.StatusMgr)

	ctx := TestContext()
	svc.StatusMgr.Start(ctx)
	svc.StatusSub.Start(ctx)

	t.Cleanup(func() {
		svc.QuerySvc.StopAll()
		svc.StatusSub.Stop()
		svc.StatusMgr.Stop()
	})

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
		tags = model.Labels{}
	}

	// Persist the worker node record in the DB so placement queries find it
	_, err := svc.DB.Exec(`INSERT INTO worker_nodes (id, max_memory_mb, max_cpu, max_storage_mb, tags) VALUES (?, ?, ?, ?, ?)`,
		nodeID, cfg.maxMemoryMB, cfg.maxCPU, cfg.maxStorageMB, tags)
	if err != nil {
		t.Fatalf("inserting worker node: %v", err)
	}

	info := orchestrator.WorkerInfo{ID: nodeID}
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
	tags         model.Labels
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

func WithTags(tags model.Labels) FakeWorkerOption {
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

// MustCreateUserToken creates a user API token and grants it access to the given gameservers.
// Each granted gameserver gets the same permission set.
func MustCreateUserToken(t *testing.T, svc *ServiceBundle, perms []string, gameserverIDs []string) string {
	t.Helper()
	raw, token, err := svc.AuthSvc.CreateUserToken("test-custom", nil, nil)
	if err != nil {
		t.Fatalf("creating custom token: %v", err)
	}
	// Add grants to each gameserver
	db := store.New(svc.DB)
	for _, gsID := range gameserverIDs {
		gs, err := db.GetGameserver(gsID)
		if err != nil || gs == nil {
			t.Fatalf("getting gameserver %s for grant: %v", gsID, err)
		}
		if gs.Grants == nil {
			gs.Grants = model.GrantMap{}
		}
		gs.Grants[token.ID] = perms
		if err := db.UpdateGameserver(gs); err != nil {
			t.Fatalf("updating gameserver %s grants: %v", gsID, err)
		}
	}
	return raw
}

// NewSettingsStore builds a settings.Store from a *sql.DB for tests that create
// SettingsService directly instead of using the full ServiceBundle.
func NewSettingsStore(db *sql.DB) settings.Store {
	return store.New(db)
}
