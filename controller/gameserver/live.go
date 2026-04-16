package gameserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/warsmite/gamejanitor/controller"
	"github.com/warsmite/gamejanitor/controller/cluster"
	"github.com/warsmite/gamejanitor/controller/event"
	"github.com/warsmite/gamejanitor/games"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/worker"
)

// Store abstracts the database operations needed by LiveGameserver.
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
	SetErrorReason(id, reason string) error
	ClearErrorReason(id string) error
	SetInstanceID(id string, instanceID *string) error
	ClearInstanceAndSetError(id string, reason string) error
	SetDesiredState(id string, state model.DesiredState) error
}

// BackupStore abstracts backup storage operations.
type BackupStore interface {
	Save(ctx context.Context, gameserverID string, backupID string, reader io.Reader) error
	Load(ctx context.Context, gameserverID string, backupID string) (io.ReadCloser, error)
	Delete(ctx context.Context, gameserverID string, backupID string) error
	SaveArchive(ctx context.Context, gameserverID string, reader io.Reader) error
	LoadArchive(ctx context.Context, gameserverID string) (io.ReadCloser, error)
	DeleteArchive(ctx context.Context, gameserverID string) error
}

// SettingsReader abstracts the settings operations needed by LiveGameserver.
type SettingsReader interface {
	GetString(key string) string
}

// ModReconciler reconciles installed mods for a gameserver after lifecycle events.
type ModReconciler interface {
	Reconcile(ctx context.Context, gameserverID string) error
}

// processState tracks the last known state of the worker process.
type processState struct {
	State    worker.InstanceState
	ExitCode int
}

// Poller abstracts start/stop polling for stats and query services.
// Implemented by cluster.StatsPoller and cluster.QueryService.
type Poller interface {
	StartPolling(gameserverID string)
	StopPolling(gameserverID string)
}

// LiveGameserver is the runtime object for a single gameserver. It owns its own
// lifecycle, status derivation, and operation tracking. One exists per gameserver
// in the Manager's map for the lifetime of the process (or until deleted).
type LiveGameserver struct {
	// Durable state — loaded from / persisted to DB via model.Gameserver.
	id                 string
	name               string
	gameID             string
	volumeName         string
	desiredState       model.DesiredState
	instanceID         *string
	installed          bool
	nodeID             *string
	ports              model.Ports
	env                model.Env
	memoryLimitMB      int
	cpuLimit           float64
	cpuEnforced        bool
	storageLimitMB     *int
	backupLimit        *int
	portMode           string
	autoRestart        *bool
	connectionAddress  *string
	nodeTags           model.Labels
	appliedConfig      *model.AppliedConfig
	createdByTokenID   *string
	grants             model.GrantMap
	sftpUsername        string
	hashedSFTPPassword string
	errorReason        string
	createdAt          time.Time
	updatedAt          time.Time

	// Runtime state — in-memory only.
	mu        sync.Mutex
	operation *model.Operation
	process   *processState
	startedAt *time.Time
	crashCount int
	cancelOp  context.CancelFunc
	opDone    chan struct{}

	// Dependencies — set at construction, not changed (except worker).
	worker        worker.Worker
	store         Store
	bus           *event.EventBus
	gameStore     *games.GameStore
	settingsSvc   SettingsReader
	modReconciler ModReconciler
	backupStore   BackupStore
	dispatcher    *cluster.Dispatcher
	placement     *cluster.PlacementService
	log           *slog.Logger

	// Progress watchers for SSE.
	watcherMu sync.RWMutex
	watchers  map[uint64]chan *model.Operation
	nextWatch uint64
}

func newLiveGameserver(gs *model.Gameserver, store Store, bus *event.EventBus, gameStore *games.GameStore, settingsSvc SettingsReader, backupStore BackupStore, dispatcher *cluster.Dispatcher, placement *cluster.PlacementService, log *slog.Logger) *LiveGameserver {
	return &LiveGameserver{
		id:                 gs.ID,
		name:               gs.Name,
		gameID:             gs.GameID,
		volumeName:         gs.VolumeName,
		desiredState:       gs.DesiredState,
		instanceID:         gs.InstanceID,
		installed:          gs.Installed,
		nodeID:             gs.NodeID,
		ports:              gs.Ports,
		env:                gs.Env,
		memoryLimitMB:      gs.MemoryLimitMB,
		cpuLimit:           gs.CPULimit,
		cpuEnforced:        gs.CPUEnforced,
		storageLimitMB:     gs.StorageLimitMB,
		backupLimit:        gs.BackupLimit,
		portMode:           gs.PortMode,
		autoRestart:        gs.AutoRestart,
		connectionAddress:  gs.ConnectionAddress,
		nodeTags:           gs.NodeTags,
		appliedConfig:      gs.AppliedConfig,
		createdByTokenID:   gs.CreatedByTokenID,
		grants:             gs.Grants,
		sftpUsername:        gs.SFTPUsername,
		hashedSFTPPassword: gs.HashedSFTPPassword,
		errorReason:        gs.ErrorReason,
		createdAt:          gs.CreatedAt,
		updatedAt:          gs.UpdatedAt,

		store:       store,
		bus:         bus,
		gameStore:   gameStore,
		settingsSvc: settingsSvc,
		backupStore: backupStore,
		dispatcher:  dispatcher,
		placement:   placement,
		log:         log.With("gameserver", gs.ID),
		watchers:  make(map[uint64]chan *model.Operation),
	}
}

// ID returns the gameserver's unique identifier.
func (g *LiveGameserver) ID() string {
	return g.id
}

// Status derives the display status from runtime state. Must be called with g.mu held.
func (g *LiveGameserver) Status() string {
	// Delete is destructive and user-initiated — show it unconditionally,
	// even for archived or unreachable gameservers, so users see their action took effect.
	if g.operation != nil && g.operation.Phase == model.PhaseDeleting {
		return controller.StatusDeleting
	}

	if g.desiredState == model.DesiredArchived {
		return controller.StatusArchived
	}

	if g.worker == nil {
		return controller.StatusUnreachable
	}

	if g.operation != nil {
		switch g.operation.Phase {
		case model.PhasePullingImage, model.PhaseDownloadingGame, model.PhaseInstalling:
			return controller.StatusInstalling
		case model.PhaseStopping:
			return controller.StatusStopping
		case model.PhaseStarting:
			return controller.StatusStarting
		case model.PhaseMigrating:
			return controller.StatusInstalling
		case model.PhaseCreatingBackup, model.PhaseRestoringBackup:
			// Backup operations don't change the display status — fall through
			// to process-based derivation below.
		}
	}

	if g.errorReason != "" {
		return controller.StatusError
	}

	if g.process != nil && g.process.State == worker.StateRunning {
		return controller.StatusRunning
	}

	return controller.StatusStopped
}

// Snapshot creates a point-in-time copy of the gameserver as a model.Gameserver.
// Acquires g.mu internally.
func (g *LiveGameserver) Snapshot() model.Gameserver {
	g.mu.Lock()
	defer g.mu.Unlock()

	gs := model.Gameserver{
		ID:                 g.id,
		Name:               g.name,
		GameID:             g.gameID,
		VolumeName:         g.volumeName,
		DesiredState:       g.desiredState,
		InstanceID:         g.instanceID,
		Installed:          g.installed,
		NodeID:             g.nodeID,
		Ports:              g.ports,
		Env:                g.env,
		MemoryLimitMB:      g.memoryLimitMB,
		CPULimit:           g.cpuLimit,
		CPUEnforced:        g.cpuEnforced,
		StorageLimitMB:     g.storageLimitMB,
		BackupLimit:        g.backupLimit,
		PortMode:           g.portMode,
		AutoRestart:        g.autoRestart,
		ConnectionAddress:  g.connectionAddress,
		NodeTags:           g.nodeTags,
		AppliedConfig:      g.appliedConfig,
		CreatedByTokenID:   g.createdByTokenID,
		Grants:             g.grants,
		SFTPUsername:       g.sftpUsername,
		HashedSFTPPassword: g.hashedSFTPPassword,
		ErrorReason:        g.errorReason,
		CreatedAt:          g.createdAt,
		UpdatedAt:          g.updatedAt,

		// Derived fields
		Status:    g.Status(),
		Operation: g.operation,
		StartedAt: g.startedAt,
	}

	// RestartRequired: compare applied config against current config
	gs.ComputeRestartRequired()

	// ConnectionHost: use override if set, otherwise Manager fills it in
	if g.connectionAddress != nil && *g.connectionAddress != "" {
		gs.ConnectionHost = *g.connectionAddress
	}

	// Populate node info from store
	g.store.PopulateNode(&gs)

	return gs
}

// HandleProcessEvent processes an instance state update from the worker.
// Stale events (wrong instance ID) are silently ignored.
func (g *LiveGameserver) HandleProcessEvent(update worker.InstanceStateUpdate) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Ignore events for stale instances
	if g.instanceID == nil || update.InstanceID != *g.instanceID {
		return
	}

	switch update.State {
	case worker.StateRunning:
		g.process = &processState{State: worker.StateRunning}
		g.startedAt = &update.StartedAt
		g.errorReason = ""
		g.store.ClearErrorReason(g.id)

		if !g.installed && update.Installed {
			g.installed = true
			dbGS, err := g.store.GetGameserver(g.id)
			if err == nil {
				dbGS.Installed = true
				g.store.UpdateGameserver(dbGS)
			}
		}

		// Start operation is complete — the worker confirmed the process is ready.
		if g.operation != nil {
			g.operation = nil
			g.notifyWatchersLocked(nil)
			g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.id, &event.OperationData{Operation: nil}))
		}

		g.bus.Publish(event.NewSystemEvent(event.EventGameserverReady, g.id, nil))
		g.bus.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, g.id, &event.StatusChangedData{
			Status: controller.StatusRunning,
		}))

	case worker.StateExited:
		// Classify the exit. It is expected (not a crash) when:
		//   - the user asked for the gameserver to stop (desiredState != model.DesiredRunning)
		//   - a delete is in progress (OpDelete intentionally kills the instance)
		// Both skip handleUnexpectedDeath so auto-restart doesn't fight the
		// intentional action.
		intentional := g.desiredState != model.DesiredRunning ||
			(g.operation != nil && g.operation.Type == model.OpDelete)
		wasRunningOrStarting := g.process != nil && (g.process.State == worker.StateRunning || g.process.State == worker.StateStarting)
		operationWasActive := g.operation != nil
		if !intentional && (wasRunningOrStarting || operationWasActive) {
			g.handleUnexpectedDeath(update.ExitCode, update.StartedAt)
		}
		// Clear any active operation — the process died
		if g.operation != nil {
			g.operation = nil
			g.notifyWatchersLocked(nil)
		}
		// Clear instanceID — the ID points to an exited instance that should not
		// block a subsequent Start from running (which would otherwise see a
		// non-nil instanceID and short-circuit).
		if g.instanceID != nil {
			g.instanceID = nil
			g.store.SetInstanceID(g.id, nil)
		}
	}
}

// SetWorker sets the worker implementation for this gameserver.
func (g *LiveGameserver) SetWorker(w worker.Worker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.worker = w
}

// ClearWorker removes the worker and clears process state (worker went offline).
func (g *LiveGameserver) ClearWorker() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.worker = nil
	g.process = nil
}

// handleUnexpectedDeath handles an instance that exited without a stop operation.
// Must be called with g.mu held.
func (g *LiveGameserver) handleUnexpectedDeath(exitCode int, startedAt time.Time) {
	reason := describeExit(exitCode, time.Since(startedAt), nil)
	g.errorReason = reason
	g.process = &processState{State: worker.StateExited, ExitCode: exitCode}
	g.store.SetErrorReason(g.id, reason)

	g.bus.Publish(event.NewSystemEvent(event.EventInstanceExited, g.id, nil))
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, g.id, &event.StatusChangedData{
		Status: controller.StatusError, ErrorReason: reason,
	}))

	if g.autoRestart == nil || !*g.autoRestart {
		return
	}

	g.crashCount++
	const maxRestartAttempts = 3
	if g.crashCount > maxRestartAttempts {
		g.errorReason = fmt.Sprintf("Crashed %d times, auto-restart disabled. Last crash: %s", g.crashCount, reason)
		g.store.SetErrorReason(g.id, g.errorReason)
		g.log.Error("auto-restart limit reached", "gameserver", g.id, "crashes", g.crashCount, "max_restarts", maxRestartAttempts)
		return
	}

	g.log.Warn("auto-restarting crashed gameserver", "gameserver", g.id, "attempt", g.crashCount, "max_restarts", maxRestartAttempts)

	// Clear error state so the restart can proceed
	g.errorReason = ""
	g.process = nil

	// Start in a new goroutine — we're inside HandleProcessEvent which holds the lock
	go func() {
		if err := g.Start(context.Background()); err != nil {
			g.log.Error("auto-restart failed", "gameserver", g.id, "error", err)
		}
	}()
}

// describeExit produces a human-readable description of a process exit.
func describeExit(exitCode int, uptime time.Duration, lastStats *event.StatsData) string {
	uptimeStr := uptime.Round(time.Second).String()
	var reason string
	switch exitCode {
	case 137:
		reason = "Killed by system (out of memory)"
		if lastStats != nil && lastStats.MemoryLimitMB > 0 {
			pct := float64(lastStats.MemoryUsageMB) / float64(lastStats.MemoryLimitMB) * 100
			reason = fmt.Sprintf("Killed by system (out of memory — was using %d/%d MB, %.0f%%). Increase memory limit.", lastStats.MemoryUsageMB, lastStats.MemoryLimitMB, pct)
		}
	case 139:
		reason = "Crashed (segmentation fault)"
	case 143:
		reason = "Terminated by signal"
	case -1:
		reason = "Killed by signal"
	case 1:
		reason = "Server exited with error (exit code 1). Check console for details."
	case 2:
		reason = "Server exited (interrupted)"
	default:
		if exitCode > 128 {
			reason = fmt.Sprintf("Killed by signal %d", exitCode-128)
		} else {
			reason = fmt.Sprintf("Server exited with code %d. Check console for details.", exitCode)
		}
	}
	return fmt.Sprintf("%s (after %s)", reason, uptimeStr)
}

// setErrorLocked sets the error reason and persists it. Must be called with g.mu held.
func (g *LiveGameserver) setErrorLocked(reason string) {
	g.errorReason = reason
	g.store.SetErrorReason(g.id, reason)
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverError, g.id, &event.ErrorData{Reason: reason}))
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverStatusChanged, g.id, &event.StatusChangedData{
		Status:      controller.StatusError,
		ErrorReason: reason,
	}))
}

// opPriority ranks operations for preemption. A submitted op whose priority
// strictly exceeds the running op's priority cancels it and takes over; equal
// or lower priority is rejected. Unlisted ops default to 0.
//
// Stop interrupts any peer (start/restart/update/…). Delete interrupts everything,
// including Stop. This encodes the real precedence users expect: "make it stop
// now" always wins over "make it do X," and "make it go away" always wins over both.
var opPriority = map[model.OpType]int{
	model.OpStop:   1,
	model.OpDelete: 2,
}

// operationOpts configures how submitOperation handles an operation.
type operationOpts struct {
	// opType is the operation type (e.g. model.OpStart).
	opType model.OpType
	// initialPhase is the operation's starting phase.
	initialPhase model.OperationPhase
	// requireWorker controls whether submitOperation checks g.worker != nil
	// before accepting. Operations that don't need a worker (unarchive placing
	// onto a different node, delete when the worker is offline) should set this false.
	requireWorker bool
	// errorPrefix is prepended to error messages when the operation fails.
	// Empty means the operation is expected to set errors itself via setError.
	errorPrefix string
	// clearOnSuccess controls whether to clear the operation when fn returns nil.
	// Lifecycle operations that end in "starting" leave the operation active
	// until HandleProcessEvent observes StateRunning — they set this false.
	// Terminal operations (stop, archive) set this true.
	clearOnSuccess bool
	// onSuccess runs after fn returns nil, before clearOperation. Used for
	// manager-level cleanup tied to operation completion (e.g. removing the
	// live object from the manager map on delete). Errors are logged, not
	// surfaced — the operation has already succeeded.
	onSuccess func(context.Context) error
}

// submitOperation is the common goroutine harness for lifecycle operations.
// It validates preconditions (applying preemption rules when a higher-priority
// op arrives), sets up the operation state, spawns a goroutine that runs fn,
// and handles cleanup (cancelOp, opDone, error reasons).
//
// On cancellation (context cancelled mid-flight), no error reason is set —
// the caller that triggered the cancellation handles that path.
func (g *LiveGameserver) submitOperation(opts operationOpts, fn func(ctx context.Context) error) error {
	incoming := opPriority[opts.opType]

	// Preempt-or-reject loop. If a running op has equal-or-higher priority we
	// reject. If ours is strictly higher, we cancel theirs and wait for its
	// goroutine to exit, then recheck — another preemption could have slipped
	// in while we were waiting.
	for {
		g.mu.Lock()
		if g.operation == nil {
			break
		}
		current := opPriority[g.operation.Type]
		if incoming <= current {
			existing := g.operation.Type
			g.mu.Unlock()
			return controller.ErrConflictf("operation %s already in progress", existing)
		}
		cancel := g.cancelOp
		done := g.opDone
		g.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
	}
	// g.mu held, g.operation == nil.

	if opts.requireWorker && g.worker == nil {
		g.mu.Unlock()
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", g.id)
	}

	g.operation = &model.Operation{Type: opts.opType, Phase: opts.initialPhase}
	op := g.operation
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	// Announce the new operation immediately so watchers and SSE consumers
	// don't have to wait for the first setPhase.
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.id, &event.OperationData{
		Operation: op,
	}))
	g.notifyWatchersLocked(op)

	go func() {
		defer func() {
			g.mu.Lock()
			g.cancelOp = nil
			done := g.opDone
			g.opDone = nil
			g.mu.Unlock()
			if done != nil {
				close(done)
			}
		}()

		err := fn(opCtx)
		if err != nil {
			g.log.Error("operation failed", "type", opts.opType, "error", err)
			if opCtx.Err() == nil && opts.errorPrefix != "" {
				g.setError(fmt.Sprintf("%s: %v", opts.errorPrefix, err))
			}
			g.clearOperation()
			return
		}
		if opts.onSuccess != nil {
			if onErr := opts.onSuccess(opCtx); onErr != nil {
				g.log.Error("operation onSuccess hook failed", "type", opts.opType, "error", onErr)
			}
		}
		if opts.clearOnSuccess {
			g.clearOperation()
		}
		// Otherwise: HandleProcessEvent clears the operation when worker reports ready
	}()

	return nil
}

// setPhase updates the current operation's phase and notifies watchers.
func (g *LiveGameserver) setPhase(phase model.OperationPhase) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.operation == nil {
		return
	}
	g.operation.Phase = phase

	g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.id, &event.OperationData{
		Operation: g.operation,
	}))
	g.notifyWatchersLocked(g.operation)
}

// setProgress updates the current operation's progress and notifies watchers.
// Does not publish to the event bus — progress updates are high-frequency.
func (g *LiveGameserver) setProgress(progress model.OperationProgress) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.operation == nil {
		return
	}
	g.operation.Progress = &progress
	g.notifyWatchersLocked(g.operation)
}

// stopInstanceOnWorker runs the graceful-then-forced teardown on the worker:
// stop-server script (best effort) → StopInstance with grace → RemoveInstance.
// Errors are logged and swallowed — callers proceed regardless, because a
// partial failure here must not leave the gameserver wedged mid-stop.
func (g *LiveGameserver) stopInstanceOnWorker(ctx context.Context, w worker.Worker, instanceID string) {
	execCtx, execCancel := context.WithTimeout(ctx, 15*time.Second)
	_, _, _, execErr := w.Exec(execCtx, instanceID, []string{"/scripts/stop-server"})
	execCancel()
	if execErr != nil {
		g.log.Info("stop-server script not available or failed", "error", execErr)
	}

	stopCtx, stopCancel := context.WithTimeout(ctx, 30*time.Second)
	if err := w.StopInstance(stopCtx, instanceID, 10); err != nil {
		g.log.Warn("failed to stop instance gracefully", "error", err)
	}
	stopCancel()

	if err := w.RemoveInstance(ctx, instanceID); err != nil {
		g.log.Warn("failed to remove instance", "error", err)
	}
}

// clearOperation clears the current operation and notifies watchers.
func (g *LiveGameserver) clearOperation() {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.operation = nil
	g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.id, &event.OperationData{
		Operation: nil,
	}))
	g.notifyWatchersLocked(nil)
}

// notifyWatchersLocked sends the operation state to all watchers.
// Non-blocking: drains and replaces if the consumer is behind.
// Must be called with g.mu held (watchers are accessed under the main lock
// or the watcherMu — here we use watcherMu for watcher-specific access).
func (g *LiveGameserver) notifyWatchersLocked(op *model.Operation) {
	g.watcherMu.RLock()
	defer g.watcherMu.RUnlock()

	for _, ch := range g.watchers {
		// Drain any pending value so we always deliver the latest
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- op:
		default:
		}
	}
}

// Watch returns a channel that receives operation updates and an unwatch function.
// The channel is buffered(1) so the latest state is always available.
func (g *LiveGameserver) Watch() (<-chan *model.Operation, func()) {
	g.watcherMu.Lock()
	defer g.watcherMu.Unlock()

	id := g.nextWatch
	g.nextWatch++
	ch := make(chan *model.Operation, 1)
	g.watchers[id] = ch

	unwatch := func() {
		g.watcherMu.Lock()
		defer g.watcherMu.Unlock()
		delete(g.watchers, id)
		close(ch)
	}

	return ch, unwatch
}

// GetOperation returns the current operation, or nil if idle.
func (g *LiveGameserver) GetOperation() *model.Operation {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.operation == nil {
		return nil
	}
	cp := *g.operation
	if g.operation.Progress != nil {
		p := *g.operation.Progress
		cp.Progress = &p
	}
	return &cp
}

// WaitForOperation blocks until the current operation goroutine finishes.
// Returns immediately if no operation is in progress.
func (g *LiveGameserver) WaitForOperation() {
	g.mu.Lock()
	done := g.opDone
	g.mu.Unlock()

	if done == nil {
		return
	}
	<-done
}

// --- Helper functions ---

// mergeEnv builds the final KEY=VALUE environment slice for an instance.
// Game defaults are applied first, then gameserver overrides on top.
func mergeEnv(game *games.Game, gs *model.Gameserver) ([]string, error) {
	env := make(map[string]string)

	// Apply game defaults
	for _, v := range game.DefaultEnv {
		if v.Default != "" {
			env[v.Key] = v.Default
		}
	}

	// Apply gameserver overrides
	for k, v := range gs.Env {
		env[k] = v
	}

	// Check required vars
	for _, v := range game.DefaultEnv {
		if v.Required {
			if val, ok := env[v.Key]; !ok || val == "" {
				label := v.Label
				if label == "" {
					label = v.Key
				}
				return nil, fmt.Errorf("required environment variable %q (%s) is not set", v.Key, label)
			}
		}
	}

	result := make([]string, 0, len(env))
	for k, v := range env {
		result = append(result, k+"="+v)
	}
	return result, nil
}

// parseGameserverPorts converts model port mappings to worker port bindings.
func parseGameserverPorts(gs *model.Gameserver) ([]worker.PortBinding, error) {
	bindings := make([]worker.PortBinding, 0, len(gs.Ports))
	for _, p := range gs.Ports {
		if int(p.HostPort) <= 0 || int(p.InstancePort) <= 0 {
			return nil, fmt.Errorf("invalid port mapping: host=%d instance=%d", int(p.HostPort), int(p.InstancePort))
		}
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		bindings = append(bindings, worker.PortBinding{
			HostPort:     int(p.HostPort),
			InstancePort: int(p.InstancePort),
			Protocol:     protocol,
		})
	}
	return bindings, nil
}

// userFriendlyError wraps an error with a user-facing prefix, stripping
// internal details that aren't helpful to end users.
func userFriendlyError(prefix string, err error) string {
	msg := err.Error()
	// Strip common Go noise
	msg = strings.TrimPrefix(msg, "rpc error: code = Unknown desc = ")
	return fmt.Sprintf("%s: %s", prefix, msg)
}

// nonAlphanumeric matches characters that aren't letters or digits.
var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]`)

// generateSFTPUsername creates a short, filesystem-safe SFTP username from
// the gameserver name. Strips non-alphanumeric chars and appends random hex.
func generateSFTPUsername(name string) string {
	clean := nonAlphanumeric.ReplaceAllString(name, "")
	clean = strings.Map(unicode.ToLower, clean)
	if len(clean) > 8 {
		clean = clean[:8]
	}
	if clean == "" {
		clean = "gs"
	}
	suffix := make([]byte, 3)
	rand.Read(suffix)
	return clean + hex.EncodeToString(suffix)
}

// generateRandomPassword creates a random hex password of the given byte length.
func generateRandomPassword(length int) string {
	pw, err := generatePassword(length)
	if err != nil {
		// crypto/rand failure is fatal — should never happen
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return pw
}

// generatePassword creates a random hex string from the given number of random bytes.
func generatePassword(length int) (string, error) {
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating random password: %w", err)
	}
	return hex.EncodeToString(b), nil
}
