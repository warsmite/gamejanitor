package gameserver

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/auth"
	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/controller/settings"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/util/naming"
	"github.com/warsmite/gamejanitor/worker"
	"golang.org/x/crypto/bcrypt"
)

// Manager holds all LiveGameserver objects and manages their lifecycle at the
// aggregate level: creation, deletion, worker routing, and recovery.
type Manager struct {
	mu          sync.RWMutex
	gameservers map[string]*LiveGameserver

	store       Store
	dispatcher  *cluster.Dispatcher
	registry    *cluster.Registry
	bus         *event.EventBus
	gameStore   *games.GameStore
	settingsSvc *settings.SettingsService
	placement   *cluster.PlacementService
	backupStore    BackupStore
	modReconciler  ModReconciler
	statsPoller    Poller
	querySvc       Poller
	sftpPort       int
	dataDir        string
	log            *slog.Logger

	// Per-worker event stream cancellation
	workerMu      sync.Mutex
	workerCancels map[string]context.CancelFunc
}

// NewManager creates a Manager. Does NOT load from DB — caller must call RecoverAll.
func NewManager(
	store Store,
	dispatcher *cluster.Dispatcher,
	registry *cluster.Registry,
	bus *event.EventBus,
	settingsSvc *settings.SettingsService,
	gameStore *games.GameStore,
	placement *cluster.PlacementService,
	backupStore BackupStore,
	dataDir string,
	sftpPort int,
	log *slog.Logger,
) *Manager {
	return &Manager{
		gameservers:   make(map[string]*LiveGameserver),
		store:         store,
		dispatcher:    dispatcher,
		registry:      registry,
		bus:           bus,
		gameStore:     gameStore,
		settingsSvc:   settingsSvc,
		placement:     placement,
		backupStore:   backupStore,
		sftpPort:      sftpPort,
		dataDir:       dataDir,
		log:           log,
		workerCancels: make(map[string]context.CancelFunc),
	}
}

// SetPollers wires stats and query pollers. Called during composition after
// the poller services are constructed. Polling is started when a gameserver
// reaches running state and stopped when it leaves.
func (m *Manager) SetPollers(statsPoller, querySvc Poller) {
	m.statsPoller = statsPoller
	m.querySvc = querySvc
}

// SetModReconciler sets the mod reconciler on all current and future gameservers.
// Called after ModService is created since it depends on services created after Manager.
func (m *Manager) SetModReconciler(r ModReconciler) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.modReconciler = r
	for _, gs := range m.gameservers {
		gs.mu.Lock()
		gs.modReconciler = r
		gs.mu.Unlock()
	}
}

// Get returns the LiveGameserver for the given ID, or nil if not found.
func (m *Manager) Get(id string) *LiveGameserver {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.gameservers[id]
}

// List returns snapshots of all gameservers matching the filter, with token
// scoping applied so non-admin tokens only see owned/granted gameservers.
func (m *Manager) List(ctx context.Context, filter model.GameserverFilter) ([]model.Gameserver, error) {
	// Token scoping: admin sees all, user tokens see owned + granted
	if token := auth.TokenFromContext(ctx); token != nil && !auth.IsAdmin(token) {
		ownedIDs, err := m.store.ListGameserverIDsByToken(token.ID)
		if err != nil {
			return nil, fmt.Errorf("listing owned gameservers: %w", err)
		}
		grantedIDs, err := m.store.ListGrantedGameserverIDs(token.ID)
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

	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []model.Gameserver
	for _, gs := range m.gameservers {
		snap := gs.Snapshot()

		// Apply filters
		if filter.GameID != nil && snap.GameID != *filter.GameID {
			continue
		}
		if filter.Status != nil && snap.Status != *filter.Status {
			continue
		}
		if filter.NodeID != nil {
			if snap.NodeID == nil || *snap.NodeID != *filter.NodeID {
				continue
			}
		}
		if len(filter.IDs) > 0 {
			found := false
			for _, id := range filter.IDs {
				if snap.ID == id {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Populate derived fields
		if snap.ConnectionHost == "" {
			if host, ok := m.settingsSvc.ResolveConnectionIP(snap.NodeID); ok {
				snap.ConnectionHost = host
			}
		}
		if m.sftpPort > 0 {
			snap.SFTPPort = m.sftpPort
		}

		result = append(result, snap)
	}

	// Populate node info on all snapshots
	if len(result) > 0 {
		m.store.PopulateNodes(result)
	}

	return result, nil
}

// Create validates, places, and persists a new gameserver, then adds it to the map.
// Returns the raw SFTP password (shown once to the user).
func (m *Manager) Create(ctx context.Context, gs *model.Gameserver) (string, error) {
	if err := gs.ValidateCreate(); err != nil {
		return "", err
	}

	gs.ID = uuid.New().String()
	gs.VolumeName = naming.VolumeName(gs.ID)
	gs.DesiredState = "stopped"

	// Set ownership from the creating token
	if token := auth.TokenFromContext(ctx); token != nil {
		gs.CreatedByTokenID = &token.ID

		if token.Role == auth.RoleUser {
			if err := enforceQuotas(m.store, token, gs); err != nil {
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

	// Resolve game aliases to canonical ID
	gs.GameID = m.gameStore.ResolveGameID(gs.GameID)

	game := m.gameStore.GetGame(gs.GameID)
	if game == nil {
		return "", controller.ErrNotFoundf("game %s not found", gs.GameID)
	}

	if err := validateRequiredEnv(game, gs); err != nil {
		return "", err
	}

	// Select node and allocate ports (serialized to prevent races).
	// Ports are tracked as "pending" until CommitPorts.
	nodeID, ports, err := m.placement.PlaceGameserver(game, gs)
	if err != nil {
		return "", err
	}
	portsCommitted := false
	defer func() {
		if !portsCommitted {
			m.placement.ReleasePorts(gs.ID)
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
		w, err := m.dispatcher.SelectWorkerByNodeID(*gs.NodeID)
		if err != nil {
			return "", controller.ErrUnavailablef("selected worker unavailable: %v", err)
		}
		targetWorker = w
	}

	if err := applyGameDefaults(gs, game); err != nil {
		return "", fmt.Errorf("applying game defaults: %w", err)
	}

	// Enforce require_* settings
	if m.settingsSvc.GetBool(settings.SettingRequireMemoryLimit) && gs.MemoryLimitMB <= 0 {
		return "", controller.ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if m.settingsSvc.GetBool(settings.SettingRequireCPULimit) && gs.CPULimit <= 0 {
		return "", controller.ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if m.settingsSvc.GetBool(settings.SettingRequireStorageLimit) && (gs.StorageLimitMB == nil || *gs.StorageLimitMB <= 0) {
		return "", controller.ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	// Warn about unlimited resources in multi-node mode
	if nodeID != "" {
		if gs.MemoryLimitMB == 0 {
			m.log.Warn("gameserver has no memory_limit_mb set, cannot account for memory in node placement", "gameserver", gs.ID)
		}
		if gs.CPULimit == 0 {
			m.log.Warn("gameserver has no cpu_limit set, cannot account for CPU in node placement", "gameserver", gs.ID)
		}
		if gs.StorageLimitMB == nil || *gs.StorageLimitMB == 0 {
			m.log.Warn("gameserver has no storage_limit_mb set, cannot account for storage in node placement", "gameserver", gs.ID)
		}
	}

	m.log.Info("creating gameserver", "gameserver", gs.ID, "name", gs.Name, "game_id", gs.GameID, "port_mode", gs.PortMode, "node_id", nodeID)

	if err := targetWorker.CreateVolume(ctx, gs.VolumeName); err != nil {
		return "", fmt.Errorf("creating volume for gameserver %s: %w", gs.ID, err)
	}

	if err := m.store.CreateGameserver(gs); err != nil {
		if rmErr := targetWorker.RemoveVolume(ctx, gs.VolumeName); rmErr != nil {
			m.log.Error("failed to clean up volume after gameserver creation failure", "volume", gs.VolumeName, "error", rmErr)
		}
		return "", err
	}
	portsCommitted = true
	m.placement.CommitPorts(gs.ID)

	// Publish create event
	actor := event.ActorFromContext(ctx)
	m.store.PopulateNode(gs)
	m.bus.Publish(event.NewEvent(event.EventGameserverCreate, gs.ID, actor, &event.GameserverActionData{
		Gameserver: gs,
	}))

	// Create the LiveGameserver and add to map
	live := newLiveGameserver(gs, m.store, m.bus, m.gameStore, m.settingsSvc, m.backupStore, m.dispatcher, m.placement, m.log)
	live.modReconciler = m.modReconciler
	if targetWorker != nil {
		live.SetWorker(targetWorker)
	}

	m.mu.Lock()
	m.gameservers[gs.ID] = live
	m.mu.Unlock()

	return rawPassword, nil
}

// Delete submits a delete operation on the gameserver. Returns nil once the
// operation is accepted — the actual teardown (worker cleanup, DB delete,
// backup store cleanup) runs in the operation goroutine and the gameserver
// disappears from the manager map via the onSuccess hook.
//
// Delete has the highest operation priority, so it preempts anything in flight
// including Stop. The stop-server script and graceful-stop timeout are
// deliberately skipped: the data is going away anyway, and running them would
// race with auto-restart on unexpected process exit.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.mu.RLock()
	live := m.gameservers[id]
	m.mu.RUnlock()

	if live == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}

	m.log.Info("deleting gameserver", "id", id)

	return live.Delete(ctx, func(ctx context.Context) error {
		// Remove the live object from the map first so subsequent lookups
		// return 404 even while the scripts-dir cleanup is running.
		m.mu.Lock()
		delete(m.gameservers, id)
		m.mu.Unlock()

		gsDir := filepath.Join(m.dataDir, "gameservers", id)
		if err := os.RemoveAll(gsDir); err != nil {
			m.log.Warn("failed to remove gameserver scripts dir", "id", id, "error", err)
		}
		return nil
	})
}

// UpdateConfig merges provided fields into the existing gameserver and persists.
// After updating the DB, updates the LiveGameserver's in-memory fields.
func (m *Manager) UpdateConfig(ctx context.Context, gs *model.Gameserver) error {
	if err := gs.ValidateUpdate(); err != nil {
		return err
	}

	existing, err := m.store.GetGameserver(gs.ID)
	if err != nil {
		return err
	}
	if existing == nil {
		return controller.ErrNotFoundf("gameserver %s not found", gs.ID)
	}

	// Per-field permission checks for non-admin, non-owner tokens
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
		installTriggered = m.installTriggeringEnvChanged(existing, gs)

		game := m.gameStore.GetGame(existing.GameID)
		if game != nil {
			if err := validateRequiredEnv(game, gs); err != nil {
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
	if m.settingsSvc.GetBool(settings.SettingRequireMemoryLimit) && existing.MemoryLimitMB <= 0 {
		return controller.ErrBadRequest("memory_limit_mb must be > 0 (require_memory_limit is enabled)")
	}
	if m.settingsSvc.GetBool(settings.SettingRequireCPULimit) && existing.CPULimit <= 0 {
		return controller.ErrBadRequest("cpu_limit must be > 0 (require_cpu_limit is enabled)")
	}
	if m.settingsSvc.GetBool(settings.SettingRequireStorageLimit) && (existing.StorageLimitMB == nil || *existing.StorageLimitMB <= 0) {
		return controller.ErrBadRequest("storage_limit_mb must be > 0 (require_storage_limit is enabled)")
	}

	m.log.Info("updating gameserver", "gameserver", gs.ID)
	if err := m.store.UpdateGameserver(existing); err != nil {
		return err
	}

	if installTriggered {
		existing.Installed = false
		if err := m.store.UpdateGameserver(existing); err != nil {
			m.log.Error("failed to clear installed flag after env change", "gameserver", gs.ID, "error", err)
		} else {
			m.log.Info("install-triggering env var changed, cleared installed flag", "gameserver", gs.ID)
		}
	}

	// Update the LiveGameserver's in-memory fields
	live := m.Get(gs.ID)
	if live != nil {
		live.mu.Lock()
		live.name = existing.Name
		live.ports = existing.Ports
		live.env = existing.Env
		live.memoryLimitMB = existing.MemoryLimitMB
		live.cpuLimit = existing.CPULimit
		live.cpuEnforced = existing.CPUEnforced
		live.backupLimit = existing.BackupLimit
		live.storageLimitMB = existing.StorageLimitMB
		live.nodeTags = existing.NodeTags
		live.connectionAddress = existing.ConnectionAddress
		live.portMode = existing.PortMode
		live.autoRestart = existing.AutoRestart
		live.grants = existing.Grants
		live.installed = existing.Installed
		live.updatedAt = existing.UpdatedAt
		live.mu.Unlock()
	}

	// Publish update event
	actor := event.ActorFromContext(ctx)
	m.store.PopulateNode(existing)
	m.bus.Publish(event.NewEvent(event.EventGameserverUpdate, existing.ID, actor, &event.GameserverActionData{
		Gameserver: existing,
	}))

	return nil
}

// installTriggeringEnvChanged checks if any env var marked with triggers_install
// has changed between the existing and updated gameserver.
func (m *Manager) installTriggeringEnvChanged(existing, updated *model.Gameserver) bool {
	game := m.gameStore.GetGame(existing.GameID)
	if game == nil {
		return false
	}

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
			m.log.Info("install-triggering env var changed", "key", key, "old", existing.Env[key], "new", updated.Env[key])
			return true
		}
	}
	return false
}

// RegenerateSFTPPassword generates a new SFTP password for a gameserver.
// Returns the raw password (shown once to the user).
func (m *Manager) RegenerateSFTPPassword(ctx context.Context, gameserverID string) (string, error) {
	gs, err := m.store.GetGameserver(gameserverID)
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
	if err := m.store.UpdateGameserver(gs); err != nil {
		return "", err
	}

	// Update the live object
	live := m.Get(gameserverID)
	if live != nil {
		live.mu.Lock()
		live.hashedSFTPPassword = string(hashed)
		live.mu.Unlock()
	}

	m.log.Info("sftp password regenerated", "gameserver", gameserverID)
	return rawPassword, nil
}

// OnWorkerOnline is called by the Registry callback when a worker registers.
// Starts event watching, assigns the worker to matching gameservers, and
// triggers recovery for gameservers on this node.
func (m *Manager) OnWorkerOnline(nodeID string, w worker.Worker) {
	m.workerMu.Lock()
	if cancel, ok := m.workerCancels[nodeID]; ok {
		cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.workerCancels[nodeID] = cancel
	m.workerMu.Unlock()

	m.log.Info("starting event watcher for worker", "worker", nodeID)
	m.watchWorkerEvents(ctx, nodeID, w)

	// Assign worker to all gameservers on this node
	m.mu.RLock()
	for _, gs := range m.gameservers {
		gs.mu.Lock()
		if gs.nodeID != nil && *gs.nodeID == nodeID {
			gs.worker = w
		}
		gs.mu.Unlock()
	}
	m.mu.RUnlock()

	m.bus.Publish(event.NewEvent(event.EventWorkerConnected, "", event.SystemActor, &event.WorkerActionData{
		WorkerID: nodeID,
	}))

	// Recover gameservers on this worker in the background
	go m.recoverWorkerGameservers(ctx, nodeID, w)
}

// OnWorkerOffline is called by the Registry callback when a worker disconnects.
// Cancels the event watcher and clears the worker reference from affected gameservers.
func (m *Manager) OnWorkerOffline(nodeID string) {
	m.workerMu.Lock()
	if cancel, ok := m.workerCancels[nodeID]; ok {
		cancel()
		delete(m.workerCancels, nodeID)
	}
	m.workerMu.Unlock()

	// Clear worker from all gameservers on this node and stop polling them
	var affectedIDs []string
	m.mu.RLock()
	for id, gs := range m.gameservers {
		gs.mu.Lock()
		if gs.nodeID != nil && *gs.nodeID == nodeID {
			gs.worker = nil
			gs.process = nil
			affectedIDs = append(affectedIDs, id)
		}
		gs.mu.Unlock()
	}
	m.mu.RUnlock()

	for _, id := range affectedIDs {
		if m.statsPoller != nil {
			m.statsPoller.StopPolling(id)
		}
		if m.querySvc != nil {
			m.querySvc.StopPolling(id)
		}
	}

	m.bus.Publish(event.NewEvent(event.EventWorkerDisconnected, "", event.SystemActor, &event.WorkerActionData{
		WorkerID: nodeID,
	}))

	m.log.Info("stopped event watcher for disconnected worker", "worker", nodeID)
}

// watchWorkerEvents starts a goroutine that watches instance state updates from
// a worker and routes them to the correct LiveGameserver. On stream error, the
// controller actively verifies whether the worker is still reachable:
//   - If verification succeeds (the stream died but the worker is alive),
//     restart the watch goroutine.
//   - If verification fails, mark the worker offline immediately — don't wait
//     for the 30s heartbeat timeout.
func (m *Manager) watchWorkerEvents(ctx context.Context, label string, w worker.Worker) {
	updateCh, errCh := w.WatchInstanceStates(ctx)

	go func() {
		m.log.Debug("watching instance states", "worker", label)
		for {
			select {
			case <-ctx.Done():
				return
			case err, ok := <-errCh:
				if !ok {
					return
				}
				m.log.Warn("instance state watcher stream broke, verifying worker health", "worker", label, "error", err)
				m.handleStreamBreak(ctx, label, w)
				return
			case update, ok := <-updateCh:
				if !ok {
					return
				}
				m.RouteProcessEvent(update)
			}
		}
	}()
}

// handleStreamBreak verifies whether a worker is still reachable after a stream
// error. If the worker responds to a health-check RPC, restart the watch loop.
// If not, mark the worker offline so gameservers don't show stale "running" status.
func (m *Manager) handleStreamBreak(ctx context.Context, label string, w worker.Worker) {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	_, err := w.GetAllInstanceStates(pingCtx)
	if err == nil {
		m.log.Info("worker still reachable after stream break, restarting watch", "worker", label)
		m.watchWorkerEvents(ctx, label, w)
		return
	}

	m.log.Warn("worker unreachable after stream break, marking offline", "worker", label, "error", err)
	m.registry.SetOffline(label)
}

// RouteProcessEvent maps a worker instance state update to the correct
// LiveGameserver and delivers it.
func (m *Manager) RouteProcessEvent(update worker.InstanceStateUpdate) {
	gsID, ok := naming.GameserverIDFromInstanceName(update.InstanceName)
	if !ok {
		return
	}

	m.mu.RLock()
	gs := m.gameservers[gsID]
	m.mu.RUnlock()

	if gs == nil {
		m.log.Debug("instance state update for unknown gameserver", "instance_name", update.InstanceName, "state", update.State)
		return
	}

	gs.HandleProcessEvent(update)

	// Start/stop polling based on the new state.
	switch update.State {
	case worker.StateRunning:
		if m.statsPoller != nil {
			m.statsPoller.StartPolling(gsID)
		}
		if m.querySvc != nil {
			m.querySvc.StartPolling(gsID)
		}
	case worker.StateExited:
		if m.statsPoller != nil {
			m.statsPoller.StopPolling(gsID)
		}
		if m.querySvc != nil {
			m.querySvc.StopPolling(gsID)
		}
	}
}

// RecoverAll loads all gameservers from the DB, creates LiveGameserver objects,
// and reconciles their state with worker reality.
func (m *Manager) RecoverAll(ctx context.Context) error {
	m.log.Info("recovering gameserver status from instance state")

	gameservers, err := m.store.ListGameservers(model.GameserverFilter{})
	if err != nil {
		return err
	}

	var withInstance, instanceMissing int

	m.mu.Lock()
	for i := range gameservers {
		gs := &gameservers[i]
		live := newLiveGameserver(gs, m.store, m.bus, m.gameStore, m.settingsSvc, m.backupStore, m.dispatcher, m.placement, m.log)
		live.modReconciler = m.modReconciler
		m.gameservers[gs.ID] = live

		w := m.dispatcher.WorkerFor(gs.ID)
		if w == nil {
			if gs.NodeID != nil {
				m.log.Warn("worker offline at startup, gameserver will show unreachable", "gameserver", gs.ID, "node_id", *gs.NodeID)
			}
			continue
		}
		live.SetWorker(w)

		if gs.InstanceID != nil {
			withInstance++
		}
		if m.recoverGameserver(ctx, live, w) {
			instanceMissing++
		}
	}
	m.mu.Unlock()

	if withInstance > 0 && instanceMissing == withInstance {
		m.log.Warn("all gameserver instances are missing — did you switch runtimes? Volumes may need manual migration",
			"expected_instances", withInstance,
		)
	}

	m.log.Info("recovery complete", "total", len(gameservers), "with_instance", withInstance, "instance_missing", instanceMissing)

	return nil
}

// recoverGameserver reconciles a LiveGameserver's state with the actual instance
// on the worker. Returns true if the gameserver had an instance ID but the
// instance was not found.
func (m *Manager) recoverGameserver(ctx context.Context, gs *LiveGameserver, w worker.Worker) bool {
	gs.mu.Lock()
	instanceID := gs.instanceID
	gs.mu.Unlock()

	if instanceID == nil {
		m.log.Info("gameserver has no instance, state is stopped", "gameserver", gs.id)
		gs.mu.Lock()
		gs.process = nil
		gs.mu.Unlock()
		return false
	}

	info, err := w.InspectInstance(ctx, *instanceID)
	if err != nil {
		m.log.Warn("instance not found, clearing", "gameserver", gs.id, "instance_id", truncID(*instanceID), "error", err)

		gs.mu.Lock()
		gs.instanceID = nil
		gs.desiredState = "stopped"
		gs.process = nil
		gs.mu.Unlock()

		// Persist to DB
		dbGS, _ := m.store.GetGameserver(gs.id)
		if dbGS != nil {
			dbGS.InstanceID = nil
			dbGS.DesiredState = "stopped"
			m.store.UpdateGameserver(dbGS)
		}
		return true
	}

	switch info.State {
	case "running":
		m.log.Info("instance running, populating process state", "gameserver", gs.id)
		gs.mu.Lock()
		gs.process = &processState{State: worker.StateRunning}
		gs.startedAt = &info.StartedAt
		gs.mu.Unlock()
		if m.statsPoller != nil {
			m.statsPoller.StartPolling(gs.id)
		}
		if m.querySvc != nil {
			m.querySvc.StartPolling(gs.id)
		}
	case "exited", "dead", "created":
		m.log.Info("instance is not running, clearing", "gameserver", gs.id, "state", info.State)

		gs.mu.Lock()
		gs.instanceID = nil
		gs.desiredState = "stopped"
		gs.process = nil
		gs.mu.Unlock()

		dbGS, _ := m.store.GetGameserver(gs.id)
		if dbGS != nil {
			dbGS.InstanceID = nil
			dbGS.DesiredState = "stopped"
			m.store.UpdateGameserver(dbGS)
		}
	default:
		m.log.Warn("instance in unexpected state", "gameserver", gs.id, "state", info.State)
		gs.mu.Lock()
		gs.process = nil
		gs.mu.Unlock()
	}

	return false
}

// recoverWorkerGameservers recovers gameservers assigned to a specific worker
// node and detects orphan instances.
func (m *Manager) recoverWorkerGameservers(ctx context.Context, nodeID string, w worker.Worker) {
	m.mu.RLock()
	knownIDs := make(map[string]bool)
	var toRecover []*LiveGameserver
	for _, gs := range m.gameservers {
		gs.mu.Lock()
		if gs.nodeID != nil && *gs.nodeID == nodeID {
			knownIDs[gs.id] = true
			if gs.instanceID != nil {
				toRecover = append(toRecover, gs)
			}
		}
		gs.mu.Unlock()
	}
	m.mu.RUnlock()

	for _, gs := range toRecover {
		m.log.Info("recovering gameserver on reconnected worker", "gameserver", gs.id, "worker", nodeID)
		m.recoverGameserver(ctx, gs, w)
	}

	m.detectOrphanInstances(ctx, nodeID, w, knownIDs)
}

// detectOrphanInstances finds gamejanitor instances running on a worker that
// aren't tracked in the database. Logged as warnings — not auto-removed, as
// they may contain player data.
func (m *Manager) detectOrphanInstances(ctx context.Context, nodeID string, w worker.Worker, knownIDs map[string]bool) {
	instances, err := w.ListGameserverInstances(ctx)
	if err != nil {
		m.log.Warn("failed to list instances for orphan detection", "worker", nodeID, "error", err)
		return
	}

	for _, c := range instances {
		if knownIDs[c.GameserverID] {
			continue
		}
		// Check gameservers on other nodes (might have been migrated)
		gs, _ := m.store.GetGameserver(c.GameserverID)
		if gs != nil {
			continue
		}
		m.log.Warn("orphan instance detected — instance exists on worker but gameserver not found in database",
			"worker", nodeID, "instance_id", truncID(c.InstanceID), "instance_name", c.InstanceName,
			"gameserver", c.GameserverID, "state", c.State)
	}
}

// truncID shortens an instance ID for log readability.
func truncID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// --- Package-level helpers ---

// validateRequiredEnv checks that all required env vars from the game definition
// are provided in the gameserver's env.
func validateRequiredEnv(game *games.Game, gs *model.Gameserver) error {
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

// enforceQuotas checks a user token's resource quotas before creating a gameserver.
func enforceQuotas(store Store, token *model.Token, gs *model.Gameserver) error {
	if token.MaxGameservers != nil {
		count, err := store.CountGameserversByToken(token.ID)
		if err != nil {
			return fmt.Errorf("checking gameserver quota: %w", err)
		}
		if count >= *token.MaxGameservers {
			return controller.ErrBadRequestf("quota exceeded: maximum %d gameservers allowed", *token.MaxGameservers)
		}
	}

	memUsed, cpuUsed, storageUsed, err := store.SumResourcesByToken(token.ID)
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

// applyGameDefaults fills in zero/empty gameserver fields from the game definition.
func applyGameDefaults(gs *model.Gameserver, game *games.Game) error {
	if len(gs.Ports) == 0 {
		gsPorts := make(model.Ports, len(game.DefaultPorts))
		for i, p := range game.DefaultPorts {
			gsPorts[i] = model.PortMapping{
				Name:         p.Name,
				HostPort:     model.FlexInt(p.Port),
				InstancePort: model.FlexInt(p.Port),
				Protocol:     p.Protocol,
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

// --- Wrapper methods for service interfaces ---
// These allow Manager to satisfy interfaces expected by backup, schedule, and proxy.

// ListGameservers is an alias for List that satisfies the proxy.GameserverLookup interface.
func (m *Manager) ListGameservers(ctx context.Context, filter model.GameserverFilter) ([]model.Gameserver, error) {
	return m.List(ctx, filter)
}

// GetGameserver returns a snapshot of the gameserver, or nil if not found.
// Satisfies proxy.GameserverLookup and backup.GameserverLifecycle.
func (m *Manager) GetGameserver(id string) (*model.Gameserver, error) {
	gs := m.Get(id)
	if gs == nil {
		return nil, nil
	}
	snap := gs.Snapshot()
	return &snap, nil
}

// Stop stops a gameserver by ID. Satisfies backup.GameserverLifecycle.
func (m *Manager) Stop(ctx context.Context, id string) error {
	gs := m.Get(id)
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	return gs.Stop(ctx)
}

// Start starts a gameserver by ID. Satisfies backup.GameserverLifecycle.
func (m *Manager) Start(ctx context.Context, id string) error {
	gs := m.Get(id)
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	return gs.Start(ctx)
}

// Restart restarts a gameserver by ID. Satisfies schedule.GameserverOps.
func (m *Manager) Restart(ctx context.Context, id string) error {
	gs := m.Get(id)
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	return gs.Restart(ctx)
}

// UpdateServerGame updates a gameserver's game by ID. Satisfies schedule.GameserverOps.
func (m *Manager) UpdateServerGame(ctx context.Context, id string) error {
	gs := m.Get(id)
	if gs == nil {
		return controller.ErrNotFoundf("gameserver %s not found", id)
	}
	return gs.UpdateServerGame(ctx)
}

// GetGameserverStats fetches live stats for a gameserver from the worker.
// Used as a fallback when the stats poller cache is empty.
func (m *Manager) GetGameserverStats(ctx context.Context, id string) (*worker.GameserverStats, error) {
	gs := m.Get(id)
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", id)
	}

	gs.mu.Lock()
	w := gs.worker
	instanceID := gs.instanceID
	volumeName := gs.volumeName
	storageLimitMB := gs.storageLimitMB
	gs.mu.Unlock()

	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	stats := &worker.GameserverStats{StorageLimitMB: storageLimitMB}

	if instanceID != nil {
		cs, err := w.InstanceStats(ctx, *instanceID)
		if err == nil {
			stats.MemoryUsageMB = cs.MemoryUsageMB
			stats.MemoryLimitMB = cs.MemoryLimitMB
			stats.CPUPercent = cs.CPUPercent
		}
	}

	volSize, err := w.VolumeSize(ctx, volumeName)
	if err == nil {
		stats.VolumeSizeBytes = volSize
	}

	return stats, nil
}

// GetInstanceLogs fetches a one-shot log snapshot from a running instance.
func (m *Manager) GetInstanceLogs(ctx context.Context, id string, tail int) (io.ReadCloser, error) {
	gs := m.Get(id)
	if gs == nil {
		return nil, controller.ErrNotFoundf("gameserver %s not found", id)
	}

	gs.mu.Lock()
	w := gs.worker
	instanceID := gs.instanceID
	gs.mu.Unlock()

	if instanceID == nil {
		return nil, fmt.Errorf("gameserver %s has no instance", id)
	}
	if w == nil {
		return nil, controller.ErrUnavailablef("worker unavailable for gameserver %s", id)
	}

	return w.InstanceLogs(ctx, *instanceID, tail, false)
}

