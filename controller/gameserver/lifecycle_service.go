package gameserver

import (
	"context"
	"log/slog"
	"strings"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/placement"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
)

// ModReconciler verifies DB-tracked mods exist on the volume before start.
type ModReconciler interface {
	Reconcile(ctx context.Context, gameserverID string) error
}

type LifecycleService struct {
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

func NewLifecycleService(
	store Store,
	dispatcher *orchestrator.Dispatcher,
	broadcaster *event.EventBus,
	settingsSvc *settings.SettingsService,
	gameStore *games.GameStore,
	placementSvc *placement.Service,
	dataDir string,
	log *slog.Logger,
) *LifecycleService {
	return &LifecycleService{
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
func (s *LifecycleService) GetGameserver(id string) (*model.Gameserver, error) {
	return s.store.GetGameserver(id)
}

func (s *LifecycleService) SetStatusProvider(sp StatusProvider) {
	s.statusProvider = sp
}

func (s *LifecycleService) SetBackupStore(store BackupStore) {
	s.backupStore = store
}

func (s *LifecycleService) SetModReconciler(r ModReconciler) {
	s.modReconciler = r
}

// getGameserverWithStatus reads a gameserver from the store and applies derived status.
func (s *LifecycleService) getGameserverWithStatus(id string) (*model.Gameserver, error) {
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
func (s *LifecycleService) setError(id string, reason string) {
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
