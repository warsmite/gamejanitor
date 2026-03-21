package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/0xkowalskidev/gamejanitor/internal/docker"
	"github.com/0xkowalskidev/gamejanitor/internal/games"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type GameserverService struct {
	db           *sql.DB
	dispatcher   *worker.Dispatcher
	log          *slog.Logger
	broadcaster  *EventBroadcaster
	readyWatcher *ReadyWatcher
	settingsSvc  *SettingsService
	gameStore    *games.GameStore
	dataDir      string
}

func NewGameserverService(db *sql.DB, dispatcher *worker.Dispatcher, broadcaster *EventBroadcaster, settingsSvc *SettingsService, gameStore *games.GameStore, dataDir string, log *slog.Logger) *GameserverService {
	return &GameserverService{db: db, dispatcher: dispatcher, broadcaster: broadcaster, settingsSvc: settingsSvc, gameStore: gameStore, dataDir: dataDir, log: log}
}

// Called after both services are created to break the circular dependency.
func (s *GameserverService) SetReadyWatcher(rw *ReadyWatcher) {
	s.readyWatcher = rw
}

func (s *GameserverService) ListGameservers(filter models.GameserverFilter) ([]models.Gameserver, error) {
	return models.ListGameservers(s.db, filter)
}

func (s *GameserverService) GetGameserver(id string) (*models.Gameserver, error) {
	return models.GetGameserver(s.db, id)
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *models.Gameserver) (string, error) {
	gs.ID = uuid.New().String()
	gs.VolumeName = docker.ContainerPrefix + gs.ID
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

	// Pick worker for placement BEFORE allocating ports so ports are scoped to the target node
	var targetWorker worker.Worker
	var nodeID string
	if gs.NodeID != nil && *gs.NodeID != "" {
		// User chose a specific node — no fallback
		nodeID = *gs.NodeID
		w, err := s.dispatcher.SelectWorkerByNodeID(nodeID)
		if err != nil {
			return "", fmt.Errorf("selected worker unavailable: %w", err)
		}
		targetWorker = w

		if err := s.checkWorkerLimits(nodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.MaxStorageMB)); err != nil {
			return "", err
		}
		if gs.PortMode == "auto" {
			allocatedPorts, err := s.AllocatePorts(game, nodeID, "")
			if err != nil {
				return "", fmt.Errorf("auto-allocating ports: %w", err)
			}
			gs.Ports = allocatedPorts
		}
	} else {
		// Try candidates in ranked order until one passes limit check + port allocation
		candidates := s.dispatcher.RankWorkersForPlacement()
		if len(candidates) == 0 {
			return "", fmt.Errorf("no workers available — connect a worker node first")
		}

		var lastErr error
		for _, c := range candidates {
			if c.NodeID != "" {
				if err := s.checkWorkerLimits(c.NodeID, gs.MemoryLimitMB, gs.CPULimit, ptrIntOr0(gs.MaxStorageMB)); err != nil {
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
			return "", fmt.Errorf("no worker has capacity for this gameserver: %w", lastErr)
		}
		if nodeID != "" {
			gs.NodeID = &nodeID
		}
	}

	if err := applyGameDefaults(gs, game); err != nil {
		return "", fmt.Errorf("applying game defaults: %w", err)
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

func (s *GameserverService) UpdateGameserver(ctx context.Context, gs *models.Gameserver) error {
	existing, err := models.GetGameserver(s.db, gs.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrNotFoundf("gameserver %s not found", gs.ID)
	}

	// Check if install-triggering env vars changed before merging
	installTriggered := false
	if gs.Env != nil {
		installTriggered = s.installTriggeringEnvChanged(existing, gs)
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

	// Cap fields: only admins (or no-auth mode) can set/change caps
	token := TokenFromContext(ctx)
	isAdmin := token == nil || IsAdmin(token)

	if isAdmin {
		if gs.MaxMemoryMB != nil {
			existing.MaxMemoryMB = gs.MaxMemoryMB
		}
		if gs.MaxCPU != nil {
			existing.MaxCPU = gs.MaxCPU
		}
		if gs.MaxBackups != nil {
			existing.MaxBackups = gs.MaxBackups
		}
		if gs.MaxStorageMB != nil {
			existing.MaxStorageMB = gs.MaxStorageMB
		}
	} else {
		// Enforce resource caps for scoped tokens
		if existing.MaxMemoryMB != nil && existing.MemoryLimitMB > *existing.MaxMemoryMB {
			return fmt.Errorf("memory_limit_mb (%d) exceeds cap (%d MB)", existing.MemoryLimitMB, *existing.MaxMemoryMB)
		}
		if existing.MaxCPU != nil && existing.CPULimit > *existing.MaxCPU {
			return fmt.Errorf("cpu_limit (%.1f) exceeds cap (%.1f cores)", existing.CPULimit, *existing.MaxCPU)
		}
	}

	s.log.Info("updating gameserver", "id", gs.ID)
	if err := models.UpdateGameserver(s.db, existing); err != nil {
		return err
	}

	if installTriggered {
		existing.Installed = false
		if err := models.UpdateGameserver(s.db, existing); err != nil {
			s.log.Error("failed to clear installed flag after env change", "id", gs.ID, "error", err)
		} else {
			s.log.Info("install-triggering env var changed, cleared installed flag", "id", gs.ID)
		}
	}

	return nil
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
	containerName := docker.ContainerPrefix + id
	if err := w.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no container to remove by name during delete", "name", containerName)
	}

	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("removing volume during delete: %w", err)
	}

	// Cascade delete schedules and backups
	if err := models.DeleteSchedulesByGameserver(s.db, id); err != nil {
		s.log.Warn("failed to delete schedules during gameserver delete", "id", id, "error", err)
	}
	if err := models.DeleteBackupsByGameserver(s.db, id); err != nil {
		s.log.Warn("failed to delete backups during gameserver delete", "id", id, "error", err)
	}

	return models.DeleteGameserver(s.db, id)
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
