package gameserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/orchestrator"
	"github.com/warsmite/gamejanitor/controller/placement"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/utilities/naming"
	"github.com/warsmite/gamejanitor/worker"
	"golang.org/x/crypto/bcrypt"
)

// Store abstracts all database operations the gameserver service needs.
type Store interface {
	ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error)
	GetGameserver(id string) (*model.Gameserver, error)
	CreateGameserver(gs *model.Gameserver) error
	UpdateGameserver(gs *model.Gameserver) error
	DeleteGameserver(id string) error
	PopulateNode(gs *model.Gameserver)
	PopulateNodes(gameservers []model.Gameserver)
	GetWorkerNode(id string) (*model.WorkerNode, error)
	AllocatedMemoryByNode(nodeID string) (int, error)
	AllocatedCPUByNode(nodeID string) (float64, error)
	AllocatedStorageByNode(nodeID string) (int, error)
	AllocatedMemoryByNodeExcluding(nodeID, excludeID string) (int, error)
	AllocatedCPUByNodeExcluding(nodeID, excludeID string) (float64, error)
	AllocatedStorageByNodeExcluding(nodeID, excludeID string) (int, error)
	ListBackups(filter model.BackupFilter) ([]model.Backup, error)
	CountGameserversByToken(tokenID string) (int, error)
	SumResourcesByToken(tokenID string) (memoryMB int, cpu float64, storageMB int, err error)
	ListGameserverIDsByToken(tokenID string) ([]string, error)
	ListGrantedGameserverIDs(tokenID string) ([]string, error)
	CreateEvent(e *model.Event) error
}

// StatusProvider derives the current display status for a gameserver from runtime state.
// Used by CRUD reads (GetGameserver, ListGameservers) to enrich the response.
type StatusProvider interface {
	DeriveStatus(gs *model.Gameserver) (status string, errorReason string)
}

// BackupStore abstracts backup file deletion for gameserver cleanup.
type BackupStore interface {
	Delete(ctx context.Context, gameserverID string, backupID string) error
	DeleteArchive(ctx context.Context, gameserverID string) error
}

type GameserverService struct {
	store          Store
	dispatcher     *orchestrator.Dispatcher
	log            *slog.Logger
	broadcaster    *event.EventBus
	statusProvider StatusProvider
	settingsSvc    *settings.SettingsService
	gameStore      *games.GameStore
	backupStore    BackupStore
	dataDir        string
	placement      *placement.Service
	operations     *Tracker
	sftpPort       int
}

func (s *GameserverService) SetOperationTracker(tracker *Tracker) {
	s.operations = tracker
}

// recordInstant publishes an instant event to EventBus for CRUD operations.
func (s *GameserverService) recordInstant(gameserverID *string, eventType string, actor json.RawMessage, data json.RawMessage) {
	if gameserverID != nil {
		gs, _ := s.store.GetGameserver(*gameserverID)
		if gs != nil {
			s.store.PopulateNode(gs)
			var a event.Actor
			json.Unmarshal(actor, &a)
			s.broadcaster.Publish(event.NewEvent(eventType, *gameserverID, a, &event.GameserverActionData{
				Gameserver: gs,
			}))
		}
	}
}

func NewGameserverService(store Store, dispatcher *orchestrator.Dispatcher, broadcaster *event.EventBus, settingsSvc *settings.SettingsService, gameStore *games.GameStore, placementSvc *placement.Service, dataDir string, sftpPort int, log *slog.Logger) *GameserverService {
	return &GameserverService{store: store, dispatcher: dispatcher, broadcaster: broadcaster, settingsSvc: settingsSvc, gameStore: gameStore, dataDir: dataDir, sftpPort: sftpPort, log: log, placement: placementSvc}
}

// Called after both services are created to break the circular dependency.
func (s *GameserverService) SetStatusProvider(sp StatusProvider) {
	s.statusProvider = sp
}

func (s *GameserverService) SetBackupStore(store BackupStore) {
	s.backupStore = store
}

func (s *GameserverService) ListGameservers(ctx context.Context, filter model.GameserverFilter) ([]model.Gameserver, error) {
	// Apply token scoping: visible = owned + granted. Admin sees all.
	if token := auth.TokenFromContext(ctx); token != nil && !auth.IsAdmin(token) {
		// Owned gameservers
		ownedIDs, err := s.store.ListGameserverIDsByToken(token.ID)
		if err != nil {
			return nil, fmt.Errorf("listing owned gameservers: %w", err)
		}
		// Granted gameservers (grants live on the gameserver, query by token ID)
		grantedIDs, err := s.store.ListGrantedGameserverIDs(token.ID)
		if err != nil {
			return nil, fmt.Errorf("listing granted gameservers: %w", err)
		}
		visibleIDs := append(ownedIDs, grantedIDs...)
		if len(visibleIDs) == 0 {
			return []model.Gameserver{}, nil
		}
		filter.IDs = auth.IntersectIDs(filter.IDs, visibleIDs)
		if len(filter.IDs) == 0 {
			return []model.Gameserver{}, nil
		}
	}

	gameservers, err := s.store.ListGameservers(filter)
	if err != nil {
		return nil, err
	}
	s.store.PopulateNodes(gameservers)
	for i := range gameservers {
		s.enrich(&gameservers[i])
	}
	return gameservers, nil
}

func (s *GameserverService) GetGameserver(id string) (*model.Gameserver, error) {
	gs, err := s.store.GetGameserver(id)
	if err != nil || gs == nil {
		return gs, err
	}
	s.store.PopulateNode(gs)
	s.enrich(gs)
	return gs, nil
}

// enrich populates all derived fields on a gameserver: status, operation,
// restart check, connection host, and SFTP port.
func (s *GameserverService) enrich(gs *model.Gameserver) {
	gs.ComputeRestartRequired()
	if s.operations != nil {
		gs.Operation = s.operations.GetOperation(gs.ID)
	}
	if s.statusProvider != nil {
		gs.Status, gs.ErrorReason = s.statusProvider.DeriveStatus(gs)
	}
	// Resolve the connection host using the same priority chain as the server:
	// gameserver override > global setting > worker external IP > worker LAN IP
	if gs.ConnectionAddress != nil && *gs.ConnectionAddress != "" {
		gs.ConnectionHost = *gs.ConnectionAddress
	} else if host, ok := s.settingsSvc.ResolveConnectionIP(gs.NodeID); ok {
		gs.ConnectionHost = host
	}
	if s.sftpPort > 0 {
		gs.SFTPPort = s.sftpPort
	}
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *model.Gameserver) (string, error) {
	if err := gs.ValidateCreate(); err != nil {
		return "", err
	}

	gs.ID = uuid.New().String()
	gs.VolumeName = naming.VolumeName(gs.ID)
	gs.DesiredState = "stopped"

	// Set ownership from the creating token
	if token := auth.TokenFromContext(ctx); token != nil {
		gs.CreatedByTokenID = &token.ID

		// Enforce quotas for user tokens
		if token.Role == auth.RoleUser {
			if err := s.enforceQuotas(token, gs); err != nil {
				return "", err
			}
		}
	}
	if gs.PortMode == "" {
		gs.PortMode = "auto"
	}
	if gs.AutoRestart == nil {
		f := false
		gs.AutoRestart = &f
	}
	gs.SFTPUsername = generateSFTPUsername(gs.Name)

	rawPassword := generateRandomPassword(16)
	hashed, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing sftp password: %w", err)
	}
	gs.HashedSFTPPassword = string(hashed)

	// Resolve game aliases to canonical ID before storing
	gs.GameID = s.gameStore.ResolveGameID(gs.GameID)

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return "", controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	// Validate required env vars from the game definition
	if err := s.validateRequiredEnv(game, gs); err != nil {
		return "", err
	}

	// Select node and allocate ports via placement service (serialized to prevent races).
	// Ports are tracked as "pending" until CommitPorts so concurrent creates don't
	// allocate the same ports.
	nodeID, ports, err := s.placement.PlaceGameserver(game, gs)
	if err != nil {
		return "", err
	}
	portsCommitted := false
	defer func() {
		if !portsCommitted {
			s.placement.ReleasePorts(gs.ID)
		}
	}()
	if ports != nil {
		gs.Ports = ports
	}
	if nodeID != "" {
		gs.NodeID = &nodeID
	}

	// Validate the selected worker is reachable
	var targetWorker worker.Worker
	if gs.NodeID != nil && *gs.NodeID != "" {
		w, err := s.dispatcher.SelectWorkerByNodeID(*gs.NodeID)
		if err != nil {
			return "", controller.ErrUnavailablef("selected worker unavailable: %v", err)
		}
		targetWorker = w
	}

	if err := applyGameDefaults(gs, game); err != nil {
		return "", fmt.Errorf("applying game defaults: %w", err)
	}

	// Enforce require_* settings
	if s.settingsSvc.GetBool(settings.SettingRequireMemoryLimit) && gs.MemoryLimitMB <= 0 {
		return "", controller.ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if s.settingsSvc.GetBool(settings.SettingRequireCPULimit) && gs.CPULimit <= 0 {
		return "", controller.ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if s.settingsSvc.GetBool(settings.SettingRequireStorageLimit) && (gs.StorageLimitMB == nil || *gs.StorageLimitMB <= 0) {
		return "", controller.ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	// Warn about unlimited resources in multi-node mode
	if nodeID != "" {
		if gs.MemoryLimitMB == 0 {
			s.log.Warn("gameserver has no memory_limit_mb set, cannot account for memory in node placement", "gameserver", gs.ID)
		}
		if gs.CPULimit == 0 {
			s.log.Warn("gameserver has no cpu_limit set, cannot account for CPU in node placement", "gameserver", gs.ID)
		}
		if gs.StorageLimitMB == nil || *gs.StorageLimitMB == 0 {
			s.log.Warn("gameserver has no storage_limit_mb set, cannot account for storage in node placement", "gameserver", gs.ID)
		}
	}

	s.log.Info("creating gameserver", "gameserver", gs.ID, "name", gs.Name, "game_id", gs.GameID, "port_mode", gs.PortMode, "node_id", nodeID)

	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return "", fmt.Errorf("creating volume for gameserver %s: %w", gs.ID, err)
	}

	if err := s.store.CreateGameserver(gs); err != nil {
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up volume after gameserver creation failure", "volume", gs.VolumeName, "error", rmErr)
		}
		return "", err
	}
	portsCommitted = true
	s.placement.CommitPorts(gs.ID)

	actor := event.ActorFromContext(ctx)
	actorJSON, _ := json.Marshal(actor)
	dataJSON, _ := json.Marshal(gs)
	s.recordInstant(&gs.ID, event.EventGameserverCreate, actorJSON, dataJSON)

	return rawPassword, nil
}

// enforceQuotas checks a user token's resource quotas before creating or updating a gameserver.
func (s *GameserverService) enforceQuotas(token *model.Token, gs *model.Gameserver) error {
	if token.MaxGameservers != nil {
		count, err := s.store.CountGameserversByToken(token.ID)
		if err != nil {
			return fmt.Errorf("checking gameserver quota: %w", err)
		}
		if count >= *token.MaxGameservers {
			return controller.ErrBadRequestf("quota exceeded: maximum %d gameservers allowed", *token.MaxGameservers)
		}
	}

	memUsed, cpuUsed, storageUsed, err := s.store.SumResourcesByToken(token.ID)
	if err != nil {
		return fmt.Errorf("checking resource quota: %w", err)
	}

	if token.MaxMemoryMB != nil && memUsed+gs.MemoryLimitMB > *token.MaxMemoryMB {
		return controller.ErrBadRequestf("quota exceeded: %d/%d MB memory used", memUsed+gs.MemoryLimitMB, *token.MaxMemoryMB)
	}
	if token.MaxCPU != nil && cpuUsed+gs.CPULimit > *token.MaxCPU {
		return controller.ErrBadRequestf("quota exceeded: %.1f/%.1f CPU used", cpuUsed+gs.CPULimit, *token.MaxCPU)
	}
	if token.MaxStorageMB != nil {
		storageMB := 0
		if gs.StorageLimitMB != nil {
			storageMB = *gs.StorageLimitMB
		}
		if storageUsed+storageMB > *token.MaxStorageMB {
			return controller.ErrBadRequestf("quota exceeded: %d/%d MB storage used", storageUsed+storageMB, *token.MaxStorageMB)
		}
	}

	return nil
}

func (s *GameserverService) RegenerateSFTPPassword(ctx context.Context, gameserverID string) (string, error) {
	gs, err := s.store.GetGameserver(gameserverID)
	if err != nil {
		return "", err
	}
	if gs == nil {
		return "", controller.ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	rawPassword := generateRandomPassword(16)
	hashed, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing sftp password: %w", err)
	}

	gs.HashedSFTPPassword = string(hashed)
	if err := s.store.UpdateGameserver(gs); err != nil {
		return "", err
	}

	s.log.Info("sftp password regenerated", "gameserver", gameserverID)
	return rawPassword, nil
}

// applyGameDefaults fills in zero/empty gameserver fields from the game definition.
func applyGameDefaults(gs *model.Gameserver, game *games.Game) error {
	// Apply default ports if none provided
	if len(gs.Ports) == 0 {
		gsPorts := make(model.Ports, len(game.DefaultPorts))
		for i, p := range game.DefaultPorts {
			gsPorts[i] = model.PortMapping{
				Name:          p.Name,
				HostPort:      model.FlexInt(p.Port),
				InstancePort: model.FlexInt(p.Port),
				Protocol:      p.Protocol,
			}
		}
		gs.Ports = gsPorts
	}

	// Merge env: start with game defaults, then overlay user-provided values
	env := make(map[string]string)
	for _, d := range game.DefaultEnv {
		if d.System {
			continue
		}
		env[d.Key] = d.Default
	}

	// User-provided env overrides defaults
	if len(gs.Env) > 0 {
		for k, v := range gs.Env {
			env[k] = v
		}
	}

	// Autogenerate values for fields where the final value is empty
	for _, d := range game.DefaultEnv {
		if d.Autogenerate == "" || d.System {
			continue
		}
		if env[d.Key] != "" {
			continue
		}
		switch d.Autogenerate {
		case "password":
			generated, err := generatePassword(16)
			if err != nil {
				return fmt.Errorf("generating password for %s: %w", d.Key, err)
			}
			env[d.Key] = generated
		default:
			return fmt.Errorf("unknown autogenerate type %q for %s", d.Autogenerate, d.Key)
		}
	}

	gs.Env = model.Env(env)

	return nil
}

func generatePassword(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b)[:length], nil
}

// UpdateGameserver merges provided fields and writes to DB.
func (s *GameserverService) UpdateGameserver(ctx context.Context, gs *model.Gameserver) error {
	if err := gs.ValidateUpdate(); err != nil {
		return err
	}

	existing, err := s.store.GetGameserver(gs.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return controller.ErrNotFoundf("gameserver %s not found", gs.ID)
	}

	// Per-field permission checks — each configure.* permission guards specific fields.
	// Owners have all permissions; granted tokens check their grant's permission list.
	token := auth.TokenFromContext(ctx)
	if token != nil && !auth.IsAdmin(token) {
		isOwner := existing.CreatedByTokenID != nil && *existing.CreatedByTokenID == token.ID
		if !isOwner {
			grantPerms, hasGrant := existing.Grants[token.ID]
			if !hasGrant {
				return controller.ErrBadRequest("no access to this gameserver")
			}
			type fieldCheck struct {
				changed bool
				perm    string
				field   string
			}
			checks := []fieldCheck{
				{gs.Name != "", auth.PermGameserverConfigureName, "name"},
				{gs.Env != nil, auth.PermGameserverConfigureEnv, "env"},
				{gs.MemoryLimitMB != 0 || gs.CPULimit != 0 || gs.StorageLimitMB != nil || gs.BackupLimit != nil || !gs.NodeTags.IsEmpty(), auth.PermGameserverConfigureResources, "resources"},
				{gs.Ports != nil || gs.PortMode != "", auth.PermGameserverConfigurePorts, "ports"},
				{gs.ConnectionAddress != nil, auth.PermGameserverConfigureConnection, "connection_address"},
				{gs.AutoRestart != nil, auth.PermGameserverConfigureAutoRestart, "auto_restart"},
			}
			for _, c := range checks {
				if c.changed && !auth.HasGrantPermission(grantPerms, c.perm) {
					return controller.ErrBadRequestf("missing permission %s to modify %s", c.perm, c.field)
				}
			}
		}
	}

	// Check if install-triggering env vars changed before merging
	installTriggered := false
	if gs.Env != nil {
		installTriggered = s.installTriggeringEnvChanged(existing, gs)

		// Validate required env vars when env is being updated
		game := s.gameStore.GetGame(existing.GameID)
		if game != nil {
			if err := s.validateRequiredEnv(game, gs); err != nil {
				return err
			}
		}
	}

	// Merge: only overwrite fields that were actually provided
	if gs.Name != "" {
		existing.Name = gs.Name
	}
	if gs.Ports != nil {
		existing.Ports = gs.Ports
	}
	if gs.Env != nil {
		existing.Env = gs.Env
	}
	if gs.MemoryLimitMB != 0 {
		existing.MemoryLimitMB = gs.MemoryLimitMB
	}
	if gs.CPULimit != 0 {
		existing.CPULimit = gs.CPULimit
		existing.CPUEnforced = gs.CPUEnforced
	}
	if gs.BackupLimit != nil {
		existing.BackupLimit = gs.BackupLimit
	}
	if gs.StorageLimitMB != nil {
		existing.StorageLimitMB = gs.StorageLimitMB
	}
	if !gs.NodeTags.IsEmpty() {
		existing.NodeTags = gs.NodeTags
	}
	if gs.ConnectionAddress != nil {
		existing.ConnectionAddress = gs.ConnectionAddress
	}
	if gs.PortMode != "" {
		existing.PortMode = gs.PortMode
	}
	if gs.AutoRestart != nil {
		existing.AutoRestart = gs.AutoRestart
	}
	if gs.Grants != nil {
		existing.Grants = gs.Grants
	}

	// Enforce require_* settings
	if s.settingsSvc.GetBool(settings.SettingRequireMemoryLimit) && existing.MemoryLimitMB <= 0 {
		return controller.ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if s.settingsSvc.GetBool(settings.SettingRequireCPULimit) && existing.CPULimit <= 0 {
		return controller.ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if s.settingsSvc.GetBool(settings.SettingRequireStorageLimit) && (existing.StorageLimitMB == nil || *existing.StorageLimitMB <= 0) {
		return controller.ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	s.log.Info("updating gameserver", "gameserver", gs.ID)
	if err := s.store.UpdateGameserver(existing); err != nil {
		return err
	}

	if installTriggered {
		existing.Installed = false
		if err := s.store.UpdateGameserver(existing); err != nil {
			s.log.Error("failed to clear installed flag after env change", "gameserver", gs.ID, "error", err)
		} else {
			s.log.Info("install-triggering env var changed, cleared installed flag", "gameserver", gs.ID)
		}
	}

	updateActor := event.ActorFromContext(ctx)
	updateActorJSON, _ := json.Marshal(updateActor)
	updateDataJSON, _ := json.Marshal(existing)
	s.recordInstant(&existing.ID, event.EventGameserverUpdate, updateActorJSON, updateDataJSON)

	return nil
}

// installTriggeringEnvChanged checks if any env var marked with triggers_install
// has changed between the existing and updated gameserver.
func (s *GameserverService) installTriggeringEnvChanged(existing, updated *model.Gameserver) bool {
	game := s.gameStore.GetGame(existing.GameID)
	if game == nil {
		return false
	}

	// Build set of keys that trigger install
	triggerKeys := make(map[string]bool)
	for _, env := range game.DefaultEnv {
		if env.TriggersInstall {
			triggerKeys[env.Key] = true
		}
	}
	if len(triggerKeys) == 0 {
		return false
	}

	for key := range triggerKeys {
		if existing.Env[key] != updated.Env[key] {
			s.log.Info("install-triggering env var changed", "key", key, "old", existing.Env[key], "new", updated.Env[key])
			return true
		}
	}
	return false
}

func (s *GameserverService) DeleteGameserver(ctx context.Context, id string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil {
		return err
	}
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("deleting gameserver", "id", id, "name", gs.Name, "desired_state", gs.DesiredState)

	return s.doDelete(ctx, id)
}

func (s *GameserverService) doDelete(ctx context.Context, id string) error {
	gs, err := s.store.GetGameserver(id)
	if err != nil || gs == nil {
		return fmt.Errorf("gameserver %s not found", id)
	}

	// Archived servers have no volume or instance on a worker — skip infrastructure cleanup
	if !gs.IsArchived() {
		w := s.dispatcher.WorkerFor(id)
		if w == nil {
			s.log.Warn("worker unavailable during delete, skipping infrastructure cleanup", "gameserver", id)
		} else {
			// Tear down running instance directly — no status/event updates needed
			// since the DB record is about to be deleted.
			if gs.InstanceID != nil {
				// Run stop-server script to flush world state before killing the process.
				// Best-effort with short timeout — don't let a failing script block delete.
				execCtx, execCancel := context.WithTimeout(ctx, 15*time.Second)
				_, _, _, execErr := w.Exec(execCtx, *gs.InstanceID, []string{"/scripts/stop-server"})
				execCancel()
				if execErr != nil {
					s.log.Info("stop-server script not available or failed during delete, proceeding", "gameserver", id, "error", execErr)
				}

				stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
				if err := w.StopInstance(stopCtx, *gs.InstanceID, 10); err != nil {
					s.log.Warn("failed to stop instance during delete", "gameserver", id, "error", err)
				}
				stopCancel()

				if err := w.RemoveInstance(ctx, *gs.InstanceID); err != nil {
					s.log.Warn("failed to remove instance by id during delete", "id", id, "error", err)
				}
			}

			// Also try removing by name in case instance_id is stale
			instanceName := naming.InstanceName(id)
			if err := w.RemoveInstance(ctx, instanceName); err != nil {
				s.log.Debug("no instance to remove by name during delete", "name", instanceName)
			}

			if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
				s.log.Warn("failed to remove volume during delete", "id", id, "error", err)
			}

			gsDir := filepath.Join(s.dataDir, "gameservers", id)
			if err := os.RemoveAll(gsDir); err != nil {
				s.log.Warn("failed to remove gameserver scripts dir", "id", id, "error", err)
			}
		}
	}

	backups, err := s.store.ListBackups(model.BackupFilter{GameserverID: id})
	if err != nil {
		s.log.Warn("failed to list backups for store cleanup", "id", id, "error", err)
	}

	s.store.PopulateNode(gs)
	deleteActor := event.ActorFromContext(ctx)
	deleteActorJSON, _ := json.Marshal(deleteActor)
	deleteDataJSON, _ := json.Marshal(gs)
	s.recordInstant(&gs.ID, event.EventGameserverDelete, deleteActorJSON, deleteDataJSON)

	if err := s.store.DeleteGameserver(id); err != nil {
		return err
	}

	if s.backupStore != nil {
		for _, b := range backups {
			if err := s.backupStore.Delete(ctx, id, b.ID); err != nil {
				s.log.Warn("failed to remove backup store file", "backup", b.ID, "error", err)
			}
		}
		if err := s.backupStore.DeleteArchive(ctx, id); err != nil {
			s.log.Warn("failed to remove archive store file", "gameserver", id, "error", err)
		}
	}

	return nil
}

func (s *GameserverService) validateRequiredEnv(game *games.Game, gs *model.Gameserver) error {
	env := gs.Env
	if env == nil {
		env = model.Env{}
	}

	for _, def := range game.DefaultEnv {
		if !def.Required && !def.ConsentRequired {
			continue
		}
		val, exists := env[def.Key]
		if !exists || val == "" || val == "false" {
			label := def.Label
			if label == "" {
				label = def.Key
			}
			if def.ConsentRequired {
				return controller.ErrBadRequestf("%s requires explicit consent and must be accepted by the end user", label)
			}
			return controller.ErrBadRequestf("%s is required", label)
		}
	}
	return nil
}

// generateSFTPUsername creates a URL-safe slug from the gameserver name with random suffix for uniqueness.
func generateSFTPUsername(name string) string {
	slug := strings.ToLower(name)
	slug = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r == ' ' || r == '-' || r == '_' {
			return '-'
		}
		return -1
	}, slug)
	// Collapse multiple hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	slug = strings.Trim(slug, "-")
	if len(slug) > 24 {
		slug = slug[:24]
	}
	if slug == "" {
		slug = "gs"
	}
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return slug + "-" + hex.EncodeToString(suffix)
}

func generateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}
