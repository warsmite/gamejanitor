package lifecycle

import (
	"context"
	"io"
	"log/slog"
	"strings"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/placement"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
)

// Store abstracts database operations the lifecycle service needs.
type Store interface {
	GetGameserver(id string) (*model.Gameserver, error)
	UpdateGameserver(gs *model.Gameserver) error
	GetWorkerNode(id string) (*model.WorkerNode, error)
}

// StatusProvider derives the current status for a gameserver from runtime state.
type StatusProvider interface {
	DeriveStatus(gs *model.Gameserver) (status string, errorReason string)
	SetRunning(gameserverID string)
	SetStopped(gameserverID string)
	ClearError(gameserverID string)
	ResetCrashCount(gameserverID string)
}

// BackupStore abstracts backup/archive file storage (local disk or S3).
type BackupStore interface {
	Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error
	Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error)
	Delete(ctx context.Context, gameserverID string, backupID string) error
	SaveArchive(ctx context.Context, gameserverID string, reader io.Reader) error
	LoadArchive(ctx context.Context, gameserverID string) (io.ReadCloser, error)
	DeleteArchive(ctx context.Context, gameserverID string) error
}

// ModReconciler verifies DB-tracked mods exist on the volume before start.
type ModReconciler interface {
	Reconcile(ctx context.Context, gameserverID string) error
}

type Service struct {
	store          Store
	dispatcher     *orchestrator.Dispatcher
	log            *slog.Logger
	broadcaster    *event.EventBus
	statusProvider StatusProvider
	modReconciler  ModReconciler
	settingsSvc    *settings.SettingsService
	gameStore      *games.GameStore
	backupStore    BackupStore
	dataDir        string
	placement      *placement.Service
}

func NewService(
	store Store,
	dispatcher *orchestrator.Dispatcher,
	broadcaster *event.EventBus,
	settingsSvc *settings.SettingsService,
	gameStore *games.GameStore,
	placementSvc *placement.Service,
	dataDir string,
	log *slog.Logger,
) *Service {
	return &Service{
		store:       store,
		dispatcher:  dispatcher,
		broadcaster: broadcaster,
		settingsSvc: settingsSvc,
		gameStore:   gameStore,
		placement:   placementSvc,
		dataDir:     dataDir,
		log:         log,
	}
}

// GetGameserver reads a gameserver from the store.
func (s *Service) GetGameserver(id string) (*model.Gameserver, error) {
	return s.store.GetGameserver(id)
}

func (s *Service) SetStatusProvider(sp StatusProvider) {
	s.statusProvider = sp
}

func (s *Service) SetBackupStore(store BackupStore) {
	s.backupStore = store
}

func (s *Service) SetModReconciler(r ModReconciler) {
	s.modReconciler = r
}

// getGameserverWithStatus reads a gameserver from the store and applies derived status.
func (s *Service) getGameserverWithStatus(id string) (*model.Gameserver, error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil || gs == nil {
		return gs, err
	}
	if s.statusProvider != nil {
		gs.Status, gs.ErrorReason = s.statusProvider.DeriveStatus(gs)
	}
	return gs, nil
}

// setError publishes an error event. The StatusManager picks it up and updates
// the in-memory runtime state. No DB write — status is derived on read.
func (s *Service) setError(id string, reason string) {
	s.broadcaster.Publish(event.NewSystemEvent(event.EventGameserverError, id, &event.ErrorData{Reason: reason}))
}

func ptrIntOr0(p *int) int {
	if p != nil {
		return *p
	}
	return 0
}

// userFriendlyError translates runtime errors into messages a user can act on.
func userFriendlyError(prefix string, err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated") {
		return "Port conflict: a port is already in use. Edit ports or stop the conflicting gameserver."
	}
	return prefix + "."
}
