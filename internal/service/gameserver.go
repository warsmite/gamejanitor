package service

import (
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
	"github.com/0xkowalskidev/gamejanitor/internal/worker"
	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/google/uuid"
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
			ContainerPort: flexInt(p.Port),
			Protocol:      p.Protocol,
		}
	}

	s.log.Info("auto-allocated ports", "game", game.ID, "base", base, "block_size", blockSize)

	return json.Marshal(result)
}

func (s *GameserverService) CreateGameserver(ctx context.Context, gs *models.Gameserver) error {
	gs.ID = uuid.New().String()
	gs.VolumeName = "gamejanitor-" + gs.ID
	gs.Status = StatusStopped

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found", gs.GameID)
	}

	// Pick worker for placement BEFORE allocating ports so ports are scoped to the target node
	targetWorker, nodeID := s.dispatcher.SelectWorkerForPlacement()
	if nodeID != "" {
		gs.NodeID = &nodeID
	}

	if gs.PortMode == "auto" {
		allocatedPorts, err := s.AllocatePorts(game, nodeID, "")
		if err != nil {
			return fmt.Errorf("auto-allocating ports: %w", err)
		}
		gs.Ports = allocatedPorts
	}

	if err := applyGameDefaults(gs, game); err != nil {
		return fmt.Errorf("applying game defaults: %w", err)
	}

	s.log.Info("creating gameserver", "id", gs.ID, "name", gs.Name, "game_id", gs.GameID, "port_mode", gs.PortMode, "node_id", nodeID)

	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return fmt.Errorf("creating volume for gameserver %s: %w", gs.ID, err)
	}

	if err := models.CreateGameserver(s.db, gs); err != nil {
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			s.log.Error("failed to clean up volume after gameserver creation failure", "volume", gs.VolumeName, "error", rmErr)
		}
		return err
	}

	return nil
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

func (s *GameserverService) UpdateGameserver(gs *models.Gameserver) error {
	existing, err := models.GetGameserver(s.db, gs.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return ErrNotFoundf("gameserver %s not found", gs.ID)
	}
	s.log.Info("updating gameserver", "id", gs.ID)
	return models.UpdateGameserver(s.db, gs)
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
		if err := w.StopContainer(ctx, *gs.ContainerID, 30); err != nil {
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

	game := s.gameStore.GetGame(gs.GameID)
	if game == nil {
		return ErrNotFoundf("game %s not found", gs.GameID)
	}

	s.log.Info("reinstalling gameserver", "id", id)

	if gs.Status != StatusStopped {
		if err := s.Stop(ctx, id); err != nil {
			return fmt.Errorf("stopping gameserver for reinstall: %w", err)
		}
	}

	w := s.dispatcher.WorkerFor(id)

	// Prepare scripts on the target worker for reinstall container
	scriptDir, _, err := w.PrepareGameScripts(ctx, gs.GameID, id)
	if err != nil {
		return fmt.Errorf("preparing scripts for reinstall: %w", err)
	}

	// Remove .installed marker via temp container
	tempName := "gamejanitor-reinstall-" + id
	tempID, err := w.CreateContainer(ctx, worker.ContainerOptions{
		Name:       tempName,
		Image:      game.BaseImage,
		Env:        []string{},
		VolumeName: gs.VolumeName,
		Binds:      []string{scriptDir + ":/scripts:ro"},
	})
	if err != nil {
		return fmt.Errorf("creating temp container for reinstall: %w", err)
	}
	defer w.RemoveContainer(ctx, tempID)

	if err := w.StartContainer(ctx, tempID); err != nil {
		return fmt.Errorf("starting temp container for reinstall: %w", err)
	}

	exitCode, _, stderr, err := w.Exec(ctx, tempID, []string{"rm", "-f", "/data/.installed"})
	if err != nil {
		return fmt.Errorf("removing install marker: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("removing install marker failed (exit %d): %s", exitCode, stderr)
	}

	if err := w.StopContainer(ctx, tempID, 10); err != nil {
		s.log.Warn("failed to stop temp reinstall container", "id", id, "error", err)
	}

	s.log.Info("install marker removed, restarting gameserver", "id", id)
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

	// Build port name → container_port map for system env var overrides
	// System env keys that end with _PORT get their value from the matching container port
	for _, d := range game.DefaultEnv {
		if !d.System {
			continue
		}
		// Find matching port: system env var default value matches a container port
		for _, p := range ports {
			if d.Default == strconv.Itoa(int(p.ContainerPort)) {
				env[d.Key] = strconv.Itoa(int(p.ContainerPort))
				break
			}
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

func (s *GameserverService) GetContainerStats(ctx context.Context, gameserverID string) (*worker.ContainerStats, error) {
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
	return s.dispatcher.WorkerFor(gameserverID).ContainerStats(ctx, *gs.ContainerID)
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
