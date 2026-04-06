package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"sync"

	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/operation"
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
	activity       *operation.ActivityTracker
	operations     *operation.Tracker
	operationWg    sync.WaitGroup
}

func NewService(
	store Store,
	dispatcher *orchestrator.Dispatcher,
	broadcaster *event.EventBus,
	settingsSvc *settings.SettingsService,
	gameStore *games.GameStore,
	placementSvc *placement.Service,
	activity *operation.ActivityTracker,
	operations *operation.Tracker,
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
		activity:    activity,
		operations:  operations,
		dataDir:     dataDir,
		log:         log,
	}
}

// GetGameserver reads a gameserver from the store. Exposed to satisfy
// backup.GameserverLifecycle interface.
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

// WaitForOperations blocks until all background lifecycle operations complete.
// Intended for tests — production code should not call this.
func (s *Service) WaitForOperations() {
	s.operationWg.Wait()
}

// WatchOperation returns a channel that streams operation state changes for a gameserver.
func (s *Service) WatchOperation(gameserverID string) (ch <-chan *model.Operation, unwatch func()) {
	if s.operations == nil {
		c := make(chan *model.Operation)
		close(c)
		return c, func() {}
	}
	return s.operations.Watch(gameserverID)
}

// GetOperationState returns the current operation for a gameserver, or nil.
func (s *Service) GetOperationState(gameserverID string) *model.Operation {
	if s.operations == nil {
		return nil
	}
	return s.operations.GetOperation(gameserverID)
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

// runOperation launches a lifecycle operation in a background goroutine.
// Captures the actor from ctx before spawning the goroutine. On completion,
// marks the activity as completed or failed.
func (s *Service) runOperation(ctx context.Context, gsID, workerID, opType string, work func(ctx context.Context) error) error {
	opID, err := s.trackActivity(ctx, gsID, workerID, opType)
	if err != nil {
		return err
	}

	actor := event.ActorFromContext(ctx)

	s.operationWg.Add(1)
	go func() {
		defer s.operationWg.Done()
		bgCtx := context.Background()
		if actor.Type != "" {
			bgCtx = event.SetActorInContext(bgCtx, actor)
		}
		if err := work(bgCtx); err != nil {
			s.log.Error("operation failed", "gameserver", gsID, "operation", opType, "error", err)
			if opID != "" {
				s.activity.Fail(gsID, err)
			}
		} else {
			if opID != "" {
				s.activity.Complete(gsID)
			}
		}
	}()
	return nil
}

func (s *Service) trackActivity(ctx context.Context, gsID, workerID, opType string) (string, error) {
	if s.activity == nil {
		return "", nil
	}
	if operation.ActivityIDFromContext(ctx) != "" {
		return "", nil
	}
	opID, err := s.activity.Start(gsID, workerID, opType, nil, nil)
	if err != nil {
		return "", err
	}

	// Publish action event to EventBus for SSE/webhook subscribers
	gs, _ := s.store.GetGameserver(gsID)
	if gs != nil {
		s.broadcaster.Publish(event.NewEvent(event.EventTypeForOp(opType), gsID, event.ActorFromContext(ctx), &event.GameserverActionData{
			Gameserver: gs,
		}))
	}

	return opID, nil
}

// recordInstant creates an event for operations that complete immediately.
func (s *Service) recordInstant(gameserverID *string, eventType string, actor json.RawMessage, data json.RawMessage) {
	if s.activity != nil {
		if err := s.activity.RecordInstant(gameserverID, eventType, actor, data); err != nil {
			s.log.Error("failed to record instant event", "type", eventType, "error", err)
		}
	}
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
