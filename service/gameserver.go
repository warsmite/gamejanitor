package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/naming"
	"github.com/warsmite/gamejanitor/models"
	"github.com/warsmite/gamejanitor/worker"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type GameserverService struct {
	db           *sql.DB
	dispatcher   *worker.Dispatcher
	log          *slog.Logger
	broadcaster  *EventBus
	readyWatcher *ReadyWatcher
	settingsSvc  *SettingsService
	gameStore    *games.GameStore
	store        BackupStore
	dataDir      string
	placementMu  sync.Mutex // serializes port allocation + gameserver creation to prevent races
}

func NewGameserverService(db *sql.DB, dispatcher *worker.Dispatcher, broadcaster *EventBus, settingsSvc *SettingsService, gameStore *games.GameStore, dataDir string, log *slog.Logger) *GameserverService {
	return &GameserverService{db: db, dispatcher: dispatcher, broadcaster: broadcaster, settingsSvc: settingsSvc, gameStore: gameStore, dataDir: dataDir, log: log}
}

// Called after both services are created to break the circular dependency.
func (s *GameserverService) SetReadyWatcher(rw *ReadyWatcher) {
	s.readyWatcher = rw
}

func (s *GameserverService) SetBackupStore(store BackupStore) {
	s.store = store
}

func (s *GameserverService) ListGameservers(filter models.GameserverFilter) ([]models.Gameserver, error) {
	gameservers, err := models.ListGameservers(s.db, filter)
	if err != nil {
		return nil, err
	}
	models.PopulateNodes(s.db, gameservers)
	return gameservers, nil
}

func (s *GameserverService) GetGameserver(id string) (*models.Gameserver, error) {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil || gs == nil {
		return gs, err
	}
	gs.PopulateNode(s.db)
	return gs, nil
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *models.Gameserver) (string, error) {
	// Serialize placement + port allocation + DB write to prevent concurrent creates
	// from allocating the same ports or overcommitting a node.
	s.placementMu.Lock()
	defer s.placementMu.Unlock()

	gs.ID = uuid.New().String()
	gs.VolumeName = naming.VolumeName(gs.ID)
	gs.Status = StatusStopped
	gs.SFTPUsername = generateSFTPUsername(gs.Name)

	rawPassword := generateRandomPassword(16)
	hashed, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing sftp password: %w", err)
	}
	gs.HashedSFTPPassword = string(hashed)

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return "", ErrNotFoundf("game %s not found", gs.GameID)
	}

	// Validate required env vars from the game definition
	if err := s.validateRequiredEnv(game, gs); err != nil {
		return "", err
	}

	var targetWorker worker.Worker
	var nodeID string
	if gs.NodeID != nil && *gs.NodeID != "" {
		// User chose a specific node — no fallback
		nodeID = *gs.NodeID
		w, err := s.dispatcher.SelectWorkerByNodeID(nodeID)
		if err != nil {
			return "", ErrUnavailablef("selected worker unavailable: %v", err)
		}
		targetWorker = w

		if err := s.checkWorkerLimits(nodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
			return "", err
		}
		if gs.PortMode == "auto" {
			allocatedPorts, err := s.AllocatePorts(game, nodeID, "")
			if err != nil {
				return "", ErrUnavailablef("no available ports for this gameserver")
			}
			gs.Ports = allocatedPorts
		}
	} else {
		// Try candidates in ranked order until one passes limit check + port allocation
		candidates := s.dispatcher.RankWorkersForPlacement(gs.NodeTags)
		if len(candidates) == 0 {
			if !gs.NodeTags.IsEmpty() {
				return "", ErrUnavailablef("no workers available with required labels %v", gs.NodeTags)
			}
			return "", ErrUnavailable("no workers available — connect a worker node first")
		}

		var lastErr error
		for _, c := range candidates {
			if c.NodeID != "" {
				if err := s.checkWorkerLimits(c.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.StorageLimitMB)); err != nil {
					s.log.Debug("worker skipped during placement", "worker_id", c.NodeID, "reason", err)
					lastErr = err
					continue
				}
			}
			if gs.PortMode == "auto" {
				allocatedPorts, err := s.AllocatePorts(game, c.NodeID, "")
				if err != nil {
					s.log.Debug("worker skipped during placement", "worker_id", c.NodeID, "reason", err)
					lastErr = err
					continue
				}
				gs.Ports = allocatedPorts
			}
			targetWorker = c.Worker
			nodeID = c.NodeID
			break
		}
		if targetWorker == nil {
			return "", ErrUnavailablef("no worker has capacity for this gameserver: %v", lastErr)
		}
		if nodeID != "" {
			gs.NodeID = &nodeID
		}
	}

	if err := applyGameDefaults(gs, game); err != nil {
		return "", fmt.Errorf("applying game defaults: %w", err)
	}

	// Enforce require_* settings
	if s.settingsSvc.GetBool(SettingRequireMemoryLimit) && gs.MemoryLimitMB <= 0 {
		return "", ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if s.settingsSvc.GetBool(SettingRequireCPULimit) && gs.CPULimit <= 0 {
		return "", ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if s.settingsSvc.GetBool(SettingRequireStorageLimit) && (gs.StorageLimitMB == nil || *gs.StorageLimitMB <= 0) {
		return "", ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	// Warn about unlimited resources in multi-node mode
	if nodeID != "" {
		if gs.MemoryLimitMB == 0 {
			s.log.Warn("gameserver has no memory_limit_mb set, cannot account for memory in node placement", "id", gs.ID)
		}
		if gs.CPULimit == 0 {
			s.log.Warn("gameserver has no cpu_limit set, cannot account for CPU in node placement", "id", gs.ID)
		}
		if gs.StorageLimitMB == nil || *gs.StorageLimitMB == 0 {
			s.log.Warn("gameserver has no storage_limit_mb set, cannot account for storage in node placement", "id", gs.ID)
		}
	}

	s.log.Info("creating gameserver", "id", gs.ID, "name", gs.Name, "game_id", gs.GameID, "port_mode", gs.PortMode, "node_id", nodeID)

	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return "", fmt.Errorf("creating volume for gameserver %s: %w", gs.ID, err)
	}

	if err := models.CreateGameserver(s.db, gs); err != nil {
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up volume after gameserver creation failure", "volume", gs.VolumeName, "error", rmErr)
		}
		return "", err
	}

	gs.PopulateNode(s.db)

	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverCreate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gs.ID,
		Gameserver:   gs,
	})

	return rawPassword, nil
}

func (s *GameserverService) RegenerateSFTPPassword(ctx context.Context, gameserverID string) (string, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return "", err
	}
	if gs == nil {
		return "", ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	rawPassword := generateRandomPassword(16)
	hashed, err := bcrypt.GenerateFromPassword([]byte(rawPassword), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing sftp password: %w", err)
	}

	gs.HashedSFTPPassword = string(hashed)
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		return "", err
	}

	s.log.Info("sftp password regenerated", "gameserver_id", gameserverID)
	return rawPassword, nil
}

// applyGameDefaults fills in zero/empty gameserver fields from the game definition.
func applyGameDefaults(gs *models.Gameserver, game *games.Game) error {
	if gs.MemoryLimitMB == 0 {
		gs.MemoryLimitMB = game.RecommendedMemoryMB
	}

	// Apply default ports if none provided
	if len(gs.Ports) == 0 || string(gs.Ports) == "null" || string(gs.Ports) == "[]" {
		gsPorts := make([]portMapping, len(game.DefaultPorts))
		for i, p := range game.DefaultPorts {
			gsPorts[i] = portMapping{
				Name:          p.Name,
				HostPort:      flexInt(p.Port),
				ContainerPort: flexInt(p.Port),
				Protocol:      p.Protocol,
			}
		}
		portsJSON, err := json.Marshal(gsPorts)
		if err != nil {
			return fmt.Errorf("marshaling default ports: %w", err)
		}
		gs.Ports = portsJSON
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
	var userEnv map[string]string
	if len(gs.Env) > 0 && string(gs.Env) != "null" && string(gs.Env) != "{}" {
		if err := json.Unmarshal(gs.Env, &userEnv); err != nil {
			return fmt.Errorf("parsing user env: %w", err)
		}
		for k, v := range userEnv {
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

	envJSON, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshaling merged env: %w", err)
	}
	gs.Env = envJSON

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
// Returns migrationTriggered=true if resources changed and the server needs to move to a different node.
func (s *GameserverService) UpdateGameserver(ctx context.Context, gs *models.Gameserver) (migrationTriggered bool, err error) {
	existing, err := models.GetGameserver(s.db, gs.ID)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, ErrNotFoundf("gameserver %s not found", gs.ID)
	}

	// Snapshot old resource values for capacity check
	oldMemory := existing.MemoryLimitMB
	oldCPU := existing.CPULimit
	oldStorage := ptrIntOr0(existing.StorageLimitMB)

	// Field-level permission guard: non-admin tokens can only change name and env
	token := TokenFromContext(ctx)
	if token != nil && !IsAdmin(token) {
		if gs.MemoryLimitMB != 0 || gs.CPULimit != 0 || gs.StorageLimitMB != nil || gs.BackupLimit != nil || gs.Ports != nil || !gs.NodeTags.IsEmpty() {
			return false, ErrBadRequestf("insufficient permissions to modify resource/placement fields")
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
				return false, err
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
	}
	existing.CPUEnforced = gs.CPUEnforced
	if gs.BackupLimit != nil {
		existing.BackupLimit = gs.BackupLimit
	}
	if gs.StorageLimitMB != nil {
		existing.StorageLimitMB = gs.StorageLimitMB
	}
	if !gs.NodeTags.IsEmpty() {
		existing.NodeTags = gs.NodeTags
	}

	// Input validation
	if existing.MemoryLimitMB < 0 {
		return false, ErrBadRequest("memory_limit_mb must be >= 0")
	}
	if existing.CPULimit < 0 {
		return false, ErrBadRequest("cpu_limit must be >= 0")
	}
	if existing.StorageLimitMB != nil && *existing.StorageLimitMB < 0 {
		return false, ErrBadRequest("storage_limit_mb must be >= 0")
	}
	if existing.BackupLimit != nil && *existing.BackupLimit < 0 {
		return false, ErrBadRequest("backup_limit must be >= 0")
	}

	// Enforce require_* settings
	if s.settingsSvc.GetBool(SettingRequireMemoryLimit) && existing.MemoryLimitMB <= 0 {
		return false, ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if s.settingsSvc.GetBool(SettingRequireCPULimit) && existing.CPULimit <= 0 {
		return false, ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if s.settingsSvc.GetBool(SettingRequireStorageLimit) && (existing.StorageLimitMB == nil || *existing.StorageLimitMB <= 0) {
		return false, ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	// Auto-migration check: if resources changed and gameserver is on a node, check capacity
	resourcesChanged := existing.MemoryLimitMB != oldMemory || existing.CPULimit != oldCPU || ptrIntOr0(existing.StorageLimitMB) != oldStorage
	needsMigration := false

	if resourcesChanged && existing.NodeID != nil && *existing.NodeID != "" {
		limitErr := s.checkWorkerLimitsExcluding(*existing.NodeID, existing.MemoryLimitMB, existing.CPULimit, ptrIntOr0(existing.StorageLimitMB), existing.ID)
		if limitErr != nil {
			// Current node can't fit — find a new one
			candidates := s.dispatcher.RankWorkersForPlacement(existing.NodeTags)

			foundNode := ""
			for _, c := range candidates {
				if c.NodeID == *existing.NodeID {
					continue // skip current node
				}
				if err := s.checkWorkerLimits(c.NodeID, existing.MemoryLimitMB, existing.CPULimit, ptrIntOr0(existing.StorageLimitMB)); err == nil {
					foundNode = c.NodeID
					break
				}
			}

			if foundNode == "" {
				reason := fmt.Sprintf("Upgrade to %d MB memory / %.1f CPU failed: no node with sufficient capacity.", existing.MemoryLimitMB, existing.CPULimit)
				s.broadcaster.Publish(GameserverErrorEvent{GameserverID: existing.ID, Reason: reason, Timestamp: time.Now()})
				return false, fmt.Errorf("%s Resource values unchanged.", reason)
			}

			needsMigration = true
			s.log.Info("auto-migration needed for resource upgrade", "id", existing.ID, "from_node", *existing.NodeID, "to_node", foundNode)

			// Write new values first, then migrate async
			if err := models.UpdateGameserver(s.db, existing); err != nil {
				return false, err
			}

			go func() {
				if err := s.MigrateGameserver(context.Background(), existing.ID, foundNode); err != nil {
					s.log.Error("auto-migration failed", "id", existing.ID, "target_node", foundNode, "error", err)
					s.broadcaster.Publish(GameserverErrorEvent{
						GameserverID: existing.ID,
						Reason:       fmt.Sprintf("Auto-migration failed: %s", err.Error()),
						Timestamp:    time.Now(),
					})
				}
			}()
		}
	}

	if !needsMigration {
		s.log.Info("updating gameserver", "id", gs.ID)
		if err := models.UpdateGameserver(s.db, existing); err != nil {
			return false, err
		}
	}

	if installTriggered {
		existing.Installed = false
		if err := models.UpdateGameserver(s.db, existing); err != nil {
			s.log.Error("failed to clear installed flag after env change", "id", gs.ID, "error", err)
		} else {
			s.log.Info("install-triggering env var changed, cleared installed flag", "id", gs.ID)
		}
	}

	existing.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverUpdate,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: existing.ID,
		Gameserver:   existing,
	})

	return needsMigration, nil
}

// installTriggeringEnvChanged checks if any env var marked with triggers_install
// has changed between the existing and updated gameserver.
func (s *GameserverService) installTriggeringEnvChanged(existing, updated *models.Gameserver) bool {
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

	// Parse old and new env
	var oldEnv, newEnv map[string]string
	if err := json.Unmarshal(existing.Env, &oldEnv); err != nil {
		return false
	}
	if err := json.Unmarshal(updated.Env, &newEnv); err != nil {
		return false
	}

	for key := range triggerKeys {
		if oldEnv[key] != newEnv[key] {
			s.log.Info("install-triggering env var changed", "key", key, "old", oldEnv[key], "new", newEnv[key])
			return true
		}
	}
	return false
}

func (s *GameserverService) DeleteGameserver(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("deleting gameserver", "id", id, "name", gs.Name)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver before delete: %w", err)
		}
		// Re-read after stop — Stop() clears ContainerID in DB
		gs, err = models.GetGameserver(s.db, id)
		if err != nil {
			return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
		}
		if gs == nil {
			return ErrNotFoundf("gameserver %s not found after stop", id)
		}
	}

	w := s.dispatcher.WorkerFor(id)
	if gs.ContainerID != nil {
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container by id during delete", "id", id, "error", err)
		}
	}
	// Also try by name in case ContainerID was cleared but container still exists
	containerName := naming.ContainerName(id)
	if err := w.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no container to remove by name during delete", "name", containerName)
	}

	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("removing volume during delete: %w", err)
	}

	// Clean up extracted scripts/defaults directory
	gsDir := filepath.Join(s.dataDir, "gameservers", id)
	if err := os.RemoveAll(gsDir); err != nil {
		s.log.Warn("failed to remove gameserver scripts dir", "id", id, "error", err)
	}

	// List backups before delete — CASCADE will remove DB records,
	// but we need the IDs to clean up store files afterward.
	backups, err := models.ListBackups(s.db, id)
	if err != nil {
		s.log.Warn("failed to list backups for store cleanup", "id", id, "error", err)
	}

	if err := models.DeleteGameserver(s.db, id); err != nil {
		return err
	}

	// Clean up backup store files (DB records already cascaded)
	for _, b := range backups {
		if err := s.store.Delete(ctx, id, b.ID); err != nil {
			s.log.Warn("failed to remove backup store file", "backup_id", b.ID, "error", err)
		}
	}

	gs.PopulateNode(s.db)
	s.broadcaster.Publish(GameserverActionEvent{
		Type:         EventGameserverDelete,
		Timestamp:    time.Now(),
		Actor:        ActorFromContext(ctx),
		GameserverID: gs.ID,
		Gameserver:   gs,
	})

	return nil
}

func (s *GameserverService) validateRequiredEnv(game *games.Game, gs *models.Gameserver) error {
	var env map[string]string
	if gs.Env != nil {
		if err := json.Unmarshal(gs.Env, &env); err != nil {
			return ErrBadRequestf("invalid env format: %v", err)
		}
	}
	if env == nil {
		env = map[string]string{}
	}

	for _, def := range game.DefaultEnv {
		if !def.Required {
			continue
		}
		val, exists := env[def.Key]
		if !exists || val == "" || val == "false" {
			label := def.Label
			if label == "" {
				label = def.Key
			}
			return ErrBadRequestf("%s is required", label)
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
