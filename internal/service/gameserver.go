package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

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

// SetReadyWatcher sets the ready watcher for log-based ready detection after start.
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

// UsedHostPorts returns the set of all host port numbers used by gameservers on a given node, excluding excludeID.
func (s *GameserverService) UsedHostPorts(nodeID string, excludeID string) (map[int]bool, error) {
	allGS, err := models.ListGameservers(s.db, models.GameserverFilter{NodeID: &nodeID})
	if err != nil {
		return nil, fmt.Errorf("listing gameservers for port check: %w", err)
	}

	used := make(map[int]bool)
	for _, gs := range allGS {
		if gs.ID == excludeID {
			continue
		}
		var ports []portMapping
		if err := json.Unmarshal(gs.Ports, &ports); err != nil {
			continue
		}
		for _, p := range ports {
			if hp := int(p.HostPort); hp != 0 {
				used[hp] = true
			}
		}
	}
	return used, nil
}

func (s *GameserverService) portRangeForNode(nodeID string) (int, int) {
	if nodeID != "" {
		node, err := models.GetWorkerNode(s.db, nodeID)
		if err == nil && node != nil && node.PortRangeStart != nil && node.PortRangeEnd != nil {
			return *node.PortRangeStart, *node.PortRangeEnd
		}
	}
	return s.settingsSvc.GetPortRangeStart(), s.settingsSvc.GetPortRangeEnd()
}

// checkWorkerLimits returns an error if the worker has exceeded its configured resource limits.
func (s *GameserverService) checkWorkerLimits(nodeID string, memoryNeeded int) error {
	node, err := models.GetWorkerNode(s.db, nodeID)
	if err != nil || node == nil {
		return nil // no node record = no limits
	}

	if node.MaxGameservers == nil && node.MaxMemoryMB == nil {
		return nil
	}

	nid := &nodeID
	existing, err := s.ListGameservers(models.GameserverFilter{NodeID: nid})
	if err != nil {
		return fmt.Errorf("checking worker limits: %w", err)
	}

	if node.MaxGameservers != nil && len(existing) >= *node.MaxGameservers {
		return fmt.Errorf("worker %s has reached its gameserver limit (%d)", nodeID, *node.MaxGameservers)
	}

	if node.MaxMemoryMB != nil {
		var allocated int
		for _, gs := range existing {
			allocated += gs.MemoryLimitMB
		}
		if allocated+memoryNeeded > *node.MaxMemoryMB {
			return fmt.Errorf("worker %s has reached its memory limit (%d MB allocated, %d MB limit)", nodeID, allocated, *node.MaxMemoryMB)
		}
	}

	return nil
}

// AllocatePorts finds a contiguous block of free host ports for the game's port requirements.
func (s *GameserverService) AllocatePorts(game *games.Game, nodeID string, excludeID string) (json.RawMessage, error) {
	gamePorts := game.DefaultPorts
	if len(gamePorts) == 0 {
		return json.RawMessage("[]"), nil
	}

	// Find unique port numbers in order
	seen := make(map[int]bool)
	var uniquePorts []int
	for _, p := range gamePorts {
		if !seen[p.Port] {
			seen[p.Port] = true
			uniquePorts = append(uniquePorts, p.Port)
		}
	}
	sort.Ints(uniquePorts)
	blockSize := len(uniquePorts)

	// Build mapping from original port number to its index (for assignment)
	portIndex := make(map[int]int)
	for i, p := range uniquePorts {
		portIndex[p] = i
	}

	rangeStart, rangeEnd := s.portRangeForNode(nodeID)

	used, err := s.UsedHostPorts(nodeID, excludeID)
	if err != nil {
		return nil, err
	}

	// Find first contiguous block of blockSize free ports
	base := -1
	for candidate := rangeStart; candidate+blockSize-1 <= rangeEnd; candidate++ {
		free := true
		for offset := 0; offset < blockSize; offset++ {
			if used[candidate+offset] {
				free = false
				candidate = candidate + offset // skip ahead
				break
			}
		}
		if free {
			base = candidate
			break
		}
	}

	if base == -1 {
		return nil, fmt.Errorf("no contiguous block of %d ports available in range %d-%d", blockSize, rangeStart, rangeEnd)
	}

	// Map game ports to allocated ports
	result := make([]portMapping, len(gamePorts))
	for i, p := range gamePorts {
		allocatedPort := base + portIndex[p.Port]
		result[i] = portMapping{
			Name:          p.Name,
			HostPort:      flexInt(allocatedPort),
			ContainerPort: flexInt(allocatedPort),
			Protocol:      p.Protocol,
		}
	}

	s.log.Info("auto-allocated ports", "game", game.ID, "base", base, "block_size", blockSize)

	return json.Marshal(result)
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *models.Gameserver) (string, error) {
	gs.ID = uuid.New().String()
	gs.VolumeName = "gamejanitor-" + gs.ID
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
		// User chose a specific node
		nodeID = *gs.NodeID
		w, err := s.dispatcher.SelectWorkerByNodeID(nodeID)
		if err != nil {
			return "", fmt.Errorf("selected worker unavailable: %w", err)
		}
		targetWorker = w
	} else {
		targetWorker, nodeID = s.dispatcher.SelectWorkerForPlacement()
		if targetWorker == nil {
			return "", fmt.Errorf("no workers available — connect a worker node first")
		}
		if nodeID != "" {
			gs.NodeID = &nodeID
		}
	}

	// Check resource limits before allocating
	if nodeID != "" {
		if err := s.checkWorkerLimits(nodeID, gs.MemoryLimitMB); err != nil {
			return "", err
		}
	}

	if gs.PortMode == "auto" {
		allocatedPorts, err := s.AllocatePorts(game, nodeID, "")
		if err != nil {
			return "", fmt.Errorf("auto-allocating ports: %w", err)
		}
		gs.Ports = allocatedPorts
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
		w := s.dispatcher.WorkerFor(gs.ID)
		if err := w.DeletePath(ctx, existing.VolumeName, ".installed"); err != nil {
			s.log.Warn("failed to clear install marker after env change", "id", gs.ID, "error", err)
		} else {
			s.log.Info("install-triggering env var changed, cleared install marker", "id", gs.ID)
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
	containerName := "gamejanitor-" + id
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

// userFriendlyError translates Docker errors into messages a user can act on.
func userFriendlyError(prefix string, err error) string {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "address already in use") || strings.Contains(msg, "port is already allocated") {
		return "Port conflict: a port is already in use. Edit ports or stop the conflicting gameserver."
	}
	return prefix + "."
}

func (s *GameserverService) Start(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	switch gs.Status {
	case StatusPulling, StatusStarting, StatusStarted, StatusRunning:
		s.log.Info("gameserver already active, skipping start", "id", id, "status", gs.Status)
		return nil
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found for gameserver %s", gs.GameID, id)
	}

	w := s.dispatcher.WorkerFor(id)

	// Pull image
	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusPulling, ""); err != nil {
		return err
	}
	if err := w.PullImage(ctx, game.BaseImage); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to pull game image. Check your internet connection.")
		return fmt.Errorf("pulling image for gameserver %s: %w", id, err)
	}

	// Merge env vars
	env, err := mergeEnv(game, gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to configure environment variables.")
		return fmt.Errorf("merging env for gameserver %s: %w", id, err)
	}

	if gs.Installed {
		env = append(env, "SKIP_INSTALL=1")
	}

	// Parse port bindings
	ports, err := parseGameserverPorts(gs)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Invalid port configuration.")
		return fmt.Errorf("parsing ports for gameserver %s: %w", id, err)
	}

	// Prepare game scripts on the target worker (extracts locally for bind-mounting)
	scriptDir, defaultsDir, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, "Failed to extract game scripts.")
		return fmt.Errorf("preparing scripts for gameserver %s: %w", id, err)
	}

	binds := []string{
		scriptDir + ":/scripts:ro",
	}
	if defaultsDir != "" {
		binds = append(binds, defaultsDir+":/defaults:ro")
	}

	// Remove old container if exists (stale from prior run/crash).
	// Always try by name in case the DB lost track of the container ID
	// (e.g. Stop cleared ContainerID but RemoveContainer failed).
	containerName := "gamejanitor-" + id
	if gs.ContainerID != nil {
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove old container by id", "id", id, "error", err)
		}
	}
	if err := w.RemoveContainer(ctx, containerName); err != nil {
		s.log.Debug("no stale container to remove by name", "name", containerName)
	}

	// Create container
	containerID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:          containerName,
		Image:         game.BaseImage,
		Env:           env,
		Ports:         ports,
		VolumeName:    gs.VolumeName,
		MemoryLimitMB: gs.MemoryLimitMB,
		CPULimit:      gs.CPULimit,
		Binds:         binds,
	})
	if err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, userFriendlyError("Failed to create container", err))
		return fmt.Errorf("creating container for gameserver %s: %w", id, err)
	}

	// Save container ID
	gs.ContainerID = &containerID
	gs.Status = StatusStarting
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		w.RemoveContainer(ctx, containerID)
		return err
	}

	// Start container
	if err := w.StartContainer(ctx, containerID); err != nil {
		setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusError, userFriendlyError("Failed to start container", err))
		return fmt.Errorf("starting container for gameserver %s: %w", id, err)
	}

	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStarted, ""); err != nil {
		return err
	}

	if s.readyWatcher != nil {
		s.readyWatcher.Watch(id, w, containerID)
	}

	s.log.Info("gameserver started", "id", id, "container_id", containerID[:12])
	return nil
}

func (s *GameserverService) Stop(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status == StatusStopped {
		s.log.Info("gameserver already stopped, skipping", "id", id)
		return nil
	}

	if err := setGameserverStatus(s.db, s.log, s.broadcaster, id, StatusStopping, ""); err != nil {
		return err
	}

	if gs.ContainerID != nil {
		w := s.dispatcher.WorkerFor(id)
		if err := w.StopContainer(ctx, *gs.ContainerID, 10); err != nil {
			s.log.Warn("failed to stop container gracefully", "id", id, "error", err)
		}
		if err := w.RemoveContainer(ctx, *gs.ContainerID); err != nil {
			s.log.Warn("failed to remove container", "id", id, "error", err)
		}
	}

	// Re-read from DB to avoid overwriting changes made by status manager during stop
	gs, err = models.GetGameserver(s.db, id)
	if err != nil {
		return fmt.Errorf("re-reading gameserver %s after stop: %w", id, err)
	}
	if gs == nil {
		return fmt.Errorf("gameserver %s not found after stop", id)
	}

	oldStatus := gs.Status
	gs.ContainerID = nil
	gs.Status = StatusStopped
	gs.ErrorReason = ""
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		return err
	}

	s.broadcaster.Publish(StatusEvent{GameserverID: id, OldStatus: oldStatus, NewStatus: StatusStopped})
	s.log.Info("gameserver stopped", "id", id)
	return nil
}

func (s *GameserverService) Restart(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	if gs.Status != StatusStopped && gs.Status != StatusError {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for restart: %w", err)
		}
	}

	return s.Start(ctx, id)
}

func (s *GameserverService) UpdateServerGame(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found", gs.GameID)
	}

	s.log.Info("updating game for gameserver", "id", id, "game", game.ID)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for update: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	// Pull latest image
	if err := w.PullImage(ctx, game.BaseImage); err != nil {
		return fmt.Errorf("pulling image for update: %w", err)
	}

	// Prepare scripts on the target worker for update container
	scriptDir, _, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		return fmt.Errorf("preparing scripts for update: %w", err)
	}
	updateBinds := []string{scriptDir + ":/scripts:ro"}

	// Run update-server in temp container
	tempName := "gamejanitor-update-" + id
	tempID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       tempName,
		Image:      game.BaseImage,
		Env:        []string{},
		VolumeName: gs.VolumeName,
		Binds:      updateBinds,
	})
	if err != nil {
		return fmt.Errorf("creating temp container for update: %w", err)
	}
	defer w.RemoveContainer(ctx, tempID)

	if err := w.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for update: %w", err)
	}

	exitCode, stdout, stderr, err := w.Exec(ctx, tempID, []string{"/scripts/update-server"})
	if err != nil {
		return fmt.Errorf("running update-server: %w", err)
	}
	if exitCode != 0 {
		s.log.Error("update-server failed", "id", id, "exit_code", exitCode, "stdout", stdout, "stderr", stderr)
		return fmt.Errorf("update-server exited with code %d", exitCode)
	}

	if err := w.StopContainer(ctx, tempID, 10); err != nil {
		s.log.Warn("failed to stop temp update container", "id", id, "error", err)
	}

	s.log.Info("game updated, restarting gameserver", "id", id)
	return s.Start(ctx, id)
}

func (s *GameserverService) Reinstall(ctx context.Context, id string) error {
	gs, err := models.GetGameserver(s.db, id)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", id)
	}

	s.log.Info("reinstalling gameserver (full wipe)", "id", id)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	// Wipe all data by removing and recreating the volume
	if err := w.RemoveVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("removing volume for reinstall: %w", err)
	}
	if err := w.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("recreating volume for reinstall: %w", err)
	}

	s.log.Info("volume wiped, starting fresh install", "id", id)
	return s.Start(ctx, id)
}


// Port mapping from gameserver's ports JSON
// flexInt handles JSON values that may be a number or a string containing a number.
type flexInt int

func (fi *flexInt) UnmarshalJSON(b []byte) error {
	// Try number first
	var n int
	if err := json.Unmarshal(b, &n); err == nil {
		*fi = flexInt(n)
		return nil
	}
	// Try string
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("cannot unmarshal %s into int or string", string(b))
	}
	if s == "" {
		*fi = 0
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return fmt.Errorf("cannot parse %q as int: %w", s, err)
	}
	*fi = flexInt(n)
	return nil
}

type portMapping struct {
	Name          string  `json:"name"`
	HostPort      flexInt `json:"host_port"`
	ContainerPort flexInt `json:"container_port"`
	Protocol      string  `json:"protocol"`
}

func mergeEnv(game *games.Game, gs *models.Gameserver) ([]string, error) {
	env := make(map[string]string)
	systemKeys := make(map[string]bool)

	for _, d := range game.DefaultEnv {
		env[d.Key] = d.Default
		if d.System {
			systemKeys[d.Key] = true
		}
	}

	// Step 2: Merge gameserver env overrides (user values win)
	var overrides map[string]string
	if err := json.Unmarshal(gs.Env, &overrides); err != nil {
		return nil, fmt.Errorf("parsing gameserver env: %w", err)
	}
	for k, v := range overrides {
		if !systemKeys[k] {
			env[k] = v
		}
	}

	// Step 3: Override system fields from port mappings
	var ports []portMapping
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return nil, fmt.Errorf("parsing gameserver ports: %w", err)
	}

	// Map game definition ports to their allocated host ports
	// System env vars whose default matches a game port get overridden with the host port
	gamePortToHost := make(map[string]int)
	for _, gp := range game.DefaultPorts {
		for _, p := range ports {
			if p.Name == gp.Name {
				gamePortToHost[strconv.Itoa(gp.Port)] = int(p.HostPort)
				break
			}
		}
	}
	for _, d := range game.DefaultEnv {
		if !d.System {
			continue
		}
		if hp, ok := gamePortToHost[d.Default]; ok {
			env[d.Key] = strconv.Itoa(hp)
		}
	}

	// Step 4: Inject MEMORY_LIMIT_MB
	if gs.MemoryLimitMB > 0 {
		env["MEMORY_LIMIT_MB"] = strconv.Itoa(gs.MemoryLimitMB)
	}

	// Step 5: Convert to []string
	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result, nil
}

func parseGameserverPorts(gs *models.Gameserver) ([]worker.PortBinding, error) {
	var ports []portMapping
	if err := json.Unmarshal(gs.Ports, &ports); err != nil {
		return nil, fmt.Errorf("parsing gameserver ports: %w", err)
	}

	bindings := make([]worker.PortBinding, len(ports))
	for i, p := range ports {
		bindings[i] = worker.PortBinding{
			HostPort:      int(p.HostPort),
			ContainerPort: int(p.ContainerPort),
			Protocol:      p.Protocol,
		}
	}
	return bindings, nil
}

func (s *GameserverService) GetContainerInfo(ctx context.Context, gameserverID string) (*worker.ContainerInfo, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).InspectContainer(ctx, *gs.ContainerID)
}

func (s *GameserverService) GetGameserverStats(ctx context.Context, gameserverID string) (*worker.GameserverStats, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	w := s.dispatcher.WorkerFor(gameserverID)
	stats := &worker.GameserverStats{
		MaxStorageMB: gs.MaxStorageMB,
	}

	// Container stats only available when running
	if gs.ContainerID != nil {
		cs, err := w.ContainerStats(ctx, *gs.ContainerID)
		if err == nil {
			stats.MemoryUsageMB = cs.MemoryUsageMB
			stats.MemoryLimitMB = cs.MemoryLimitMB
			stats.CPUPercent = cs.CPUPercent
		} else {
			s.log.Debug("container stats unavailable", "gameserver_id", gameserverID, "error", err)
		}
	}

	// Volume size always available (only needs volume name)
	volSize, err := w.VolumeSize(ctx, gs.VolumeName)
	if err != nil {
		s.log.Debug("volume size unavailable", "gameserver_id", gameserverID, "error", err)
	} else {
		stats.VolumeSizeBytes = volSize
	}

	return stats, nil
}

func (s *GameserverService) GetVolumeSize(ctx context.Context, gameserverID string) (int64, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return 0, err
	}
	if gs == nil {
		return 0, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).VolumeSize(ctx, gs.VolumeName)
}

// MigrateGameserver moves a gameserver from its current node to a different node.
// Requires both source and target workers to be online.
func (s *GameserverService) MigrateGameserver(ctx context.Context, gameserverID string, targetNodeID string) error {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return err
	}
	if gs == nil {
		return ErrNotFoundf("gameserver %s not found", gameserverID)
	}

	currentNodeID := ""
	if gs.NodeID != nil {
		currentNodeID = *gs.NodeID
	}
	if currentNodeID == targetNodeID {
		return fmt.Errorf("gameserver is already on node %s", targetNodeID)
	}

	// Validate target worker is connected
	targetWorker, err := s.dispatcher.SelectWorkerByNodeID(targetNodeID)
	if err != nil {
		return fmt.Errorf("target worker unavailable: %w", err)
	}

	// Check target node limits
	if err := s.checkWorkerLimits(targetNodeID, gs.MemoryLimitMB); err != nil {
		return err
	}

	// Get source worker (must be online to transfer data)
	sourceWorker := s.dispatcher.WorkerFor(gameserverID)
	if sourceWorker == nil {
		return fmt.Errorf("source worker is offline, cannot migrate (both workers must be online)")
	}

	s.log.Info("migrating gameserver", "id", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)

	// Stop if running
	if gs.Status != StatusStopped {
		s.log.Info("stopping gameserver for migration", "id", gameserverID)
		if err := s.Stop(ctx, gameserverID); err != nil {
			return fmt.Errorf("stopping gameserver for migration: %w", err)
		}
	}

	// Tar volume from source — fully buffer before modifying target to avoid
	// issues if source and target share a Docker daemon (same-host migration)
	s.log.Info("transferring volume data", "id", gameserverID, "volume", gs.VolumeName)
	tarReader, err := sourceWorker.BackupVolume(ctx, gs.VolumeName)
	if err != nil {
		return fmt.Errorf("reading volume from source worker: %w", err)
	}
	var tarBuf bytes.Buffer
	if _, err := io.Copy(&tarBuf, tarReader); err != nil {
		tarReader.Close()
		return fmt.Errorf("buffering volume data: %w", err)
	}
	tarReader.Close()
	s.log.Info("volume data buffered", "id", gameserverID, "size_bytes", tarBuf.Len())

	// Create volume on target and restore
	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("creating volume on target worker: %w", err)
	}

	if err := targetWorker.RestoreVolume(ctx, gs.VolumeName, &tarBuf); err != nil {
		// Clean up the volume we just created
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up target volume after failed restore", "volume", gs.VolumeName, "error", rmErr)
		}
		return fmt.Errorf("restoring volume on target worker: %w", err)
	}

	// Reallocate ports on target node's range
	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return fmt.Errorf("game %s not found", gs.GameID)
	}
	newPorts, err := s.AllocatePorts(game, targetNodeID, "")
	if err != nil {
		return fmt.Errorf("allocating ports on target node: %w", err)
	}

	// Update DB: node_id and ports
	gs.NodeID = &targetNodeID
	gs.Ports = newPorts
	if err := models.UpdateGameserver(s.db, gs); err != nil {
		return fmt.Errorf("updating gameserver node assignment: %w", err)
	}

	// Clean up old volume on source worker
	if err := sourceWorker.RemoveVolume(ctx, gs.VolumeName); err != nil {
		s.log.Warn("failed to remove old volume from source worker", "volume", gs.VolumeName, "error", err)
	}

	s.log.Info("gameserver migrated", "id", gameserverID, "from_node", currentNodeID, "to_node", targetNodeID)
	return nil
}

func (s *GameserverService) GetContainerLogs(ctx context.Context, gameserverID string, tail int) (io.ReadCloser, error) {
	gs, err := models.GetGameserver(s.db, gameserverID)
	if err != nil {
		return nil, err
	}
	if gs == nil {
		return nil, ErrNotFoundf("gameserver %s not found", gameserverID)
	}
	if gs.ContainerID == nil {
		return nil, fmt.Errorf("gameserver %s has no container", gameserverID)
	}
	return s.dispatcher.WorkerFor(gameserverID).ContainerLogs(ctx, *gs.ContainerID, tail, false)
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
