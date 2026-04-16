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

// Poller abstracts start/stop polling for stats and query services.
// Implemented by cluster.StatsPoller and cluster.QueryService.
type Poller interface {
	StartPolling(gameserverID string)
	StopPolling(gameserverID string)
}

// LiveGameserver is the runtime object for a single gameserver. It owns its own
// lifecycle and operation tracking. One exists per gameserver in the Manager's
// map for the lifetime of the process (or until deleted).
//
// Durable state lives on `spec` (a *model.Gameserver). Mutations to durable
// fields go through `spec` and are persisted via g.store methods. Observed
// facts (process state, operation, runtime timestamps) live as top-level
// fields on the struct — they are in-memory only and never persisted.
type LiveGameserver struct {
	// Spec — durable state. The whole model.Gameserver is held here; writes
	// are either local-only or paired with a store call. Reading spec fields
	// requires g.mu; writing spec fields requires g.mu.
	spec *model.Gameserver

	// Runtime state — in-memory only.
	mu         sync.Mutex
	operation  *model.Operation
	cancelOp   context.CancelFunc
	opDone     chan struct{}
	crashCount int

	// Observed process facts — populated by HandleProcessEvent from worker
	// events. ProcessState and Ready are orthogonal: a Running process is
	// alive on the worker; Ready is true when the readiness signal fired.
	processState model.ProcessState
	ready        bool
	startedAt    *time.Time // when processState became Running
	readyAt      *time.Time // when ready became true
	exitedAt     *time.Time // when processState became Exited
	exitCode     int        // meaningful only when processState == Exited

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

func newLiveGameserver(gs *model.Gameserver, store Store, bus *event.EventBus, gameStore *games.GameStore, settingsSvc SettingsReader, modReconciler ModReconciler, backupStore BackupStore, dispatcher *cluster.Dispatcher, placement *cluster.PlacementService, log *slog.Logger) *LiveGameserver {
	return &LiveGameserver{
		spec:          gs,
		processState:  model.ProcessNone,
		store:         store,
		bus:           bus,
		gameStore:     gameStore,
		settingsSvc:   settingsSvc,
		modReconciler: modReconciler,
		backupStore:   backupStore,
		dispatcher:    dispatcher,
		placement:     placement,
		log:           log.With("gameserver", gs.ID),
		watchers:      make(map[uint64]chan *model.Operation),
	}
}

// ID returns the gameserver's unique identifier.
func (g *LiveGameserver) ID() string {
	return g.spec.ID
}

// Snapshot returns a point-in-time view of the gameserver — spec plus
// observed primary facts — as a model.Gameserver. Consumers that want a
// one-word display pill derive it from these fields themselves; the controller
// does not compress reality into a status enum.
func (g *LiveGameserver) Snapshot() model.Gameserver {
	g.mu.Lock()
	defer g.mu.Unlock()

	// Shallow-copy the spec and overlay observed runtime facts. Any slices
	// or maps inside (Ports, Env, Grants) remain shared with the spec —
	// callers must not mutate them, matching the pre-existing contract.
	gs := *g.spec
	gs.Operation = g.operation
	gs.ProcessState = g.processState
	gs.Ready = g.ready
	gs.WorkerOnline = g.worker != nil
	gs.ExitCode = g.exitCode
	gs.StartedAt = g.startedAt
	gs.ReadyAt = g.readyAt
	gs.ExitedAt = g.exitedAt

	gs.ComputeRestartRequired()
	if g.spec.ConnectionAddress != nil && *g.spec.ConnectionAddress != "" {
		gs.ConnectionHost = *g.spec.ConnectionAddress
	}
	g.store.PopulateNode(&gs)

	return gs
}

// HandleProcessEvent processes an instance state update from the worker. All
// state mutations happen under g.mu; store writes, bus publishes, and
// watcher notifications are deferred and executed after the lock is released.
//
// Stale events (wrong instance ID) are silently ignored. Worker state and
// ready flags are orthogonal: both map onto the gameserver's observed fields.
func (g *LiveGameserver) HandleProcessEvent(update worker.InstanceStateUpdate) {
	// Side effects to perform after releasing the lock. Populated while the
	// lock is held, flushed after we unlock. This is the pattern that keeps
	// bus publishes and store writes off the hot path of other callers.
	var se processEventSideEffects

	g.mu.Lock()

	if g.spec.InstanceID == nil || update.InstanceID != *g.spec.InstanceID {
		g.mu.Unlock()
		return
	}

	switch update.State {
	case worker.StateRunning:
		wasReady := g.ready
		g.setProcessRunningLocked(update)
		if g.spec.ErrorReason != "" {
			g.spec.ErrorReason = ""
			se.clearError = true
		}
		if !g.spec.Installed && update.Installed {
			g.spec.Installed = true
			se.markInstalled = true
		}
		// A ready transition completes the start operation — the process is
		// up AND the readiness signal has fired. Process-alive without ready
		// keeps the operation active.
		if g.ready && !wasReady {
			if g.operation != nil {
				g.operation = nil
				se.publishOperationCleared = true
			}
			se.publishReady = true
		}

	case worker.StateExited:
		// Classify the exit. It's expected (not a crash) when the user asked
		// the gameserver to stop (desiredState != running) or a delete is in
		// progress. Expected exits skip the crash accounting so auto-restart
		// doesn't fight intentional teardown.
		intentional := g.spec.DesiredState != model.DesiredRunning ||
			(g.operation != nil && g.operation.Type == model.OpDelete)
		wasRunning := g.processState == model.ProcessRunning
		operationWasActive := g.operation != nil

		if !intentional && (wasRunning || operationWasActive) {
			reason := describeExit(update.ExitCode, time.Since(update.StartedAt), nil)
			g.spec.ErrorReason = reason
			g.setProcessExitedLocked(update.ExitCode, update.ExitedAt)
			se.persistErrorReason = reason
			se.publishInstanceExited = true

			// Auto-restart decision.
			const maxRestartAttempts = 3
			if g.spec.AutoRestart != nil && *g.spec.AutoRestart {
				g.crashCount++
				if g.crashCount > maxRestartAttempts {
					g.spec.ErrorReason = fmt.Sprintf("Crashed %d times, auto-restart disabled. Last crash: %s", g.crashCount, reason)
					se.persistErrorReason = g.spec.ErrorReason
					se.publishError = g.spec.ErrorReason
					se.crashLimitReached = true
					se.crashCount = g.crashCount
				} else {
					// Clear error state so the restart can proceed cleanly.
					g.spec.ErrorReason = ""
					g.clearProcessLocked()
					se.persistErrorReason = ""
					se.clearError = true
					se.publishError = reason // surface the crash reason even though we're auto-restarting
					se.triggerAutoRestart = true
					se.crashCount = g.crashCount
				}
			} else {
				se.publishError = reason
			}
		} else {
			g.setProcessExitedLocked(update.ExitCode, update.ExitedAt)
		}
		if g.operation != nil {
			g.operation = nil
			se.publishOperationCleared = true
		}
		// Clear instanceID — the ID points to an exited instance that should
		// not block a subsequent Start from running.
		if g.spec.InstanceID != nil {
			g.spec.InstanceID = nil
			se.clearInstanceID = true
		}
	}

	g.mu.Unlock()

	g.applyProcessEventSideEffects(se)
}

// processEventSideEffects captures work queued under g.mu that must execute
// after the lock is released — store writes, bus publishes, and the auto-restart
// goroutine. Keeping these off the hot path prevents a slow event-bus
// subscriber or a slow store write from serializing other operations on this
// gameserver.
type processEventSideEffects struct {
	markInstalled           bool
	clearInstanceID         bool
	clearError              bool
	persistErrorReason      string
	publishOperationCleared bool
	publishReady            bool
	publishInstanceExited   bool
	publishError            string
	triggerAutoRestart      bool
	crashLimitReached       bool
	crashCount              int
}

func (g *LiveGameserver) applyProcessEventSideEffects(se processEventSideEffects) {
	if se.clearError {
		g.store.ClearErrorReason(g.spec.ID)
	} else if se.persistErrorReason != "" {
		g.store.SetErrorReason(g.spec.ID, se.persistErrorReason)
	}
	if se.markInstalled {
		if dbGS, err := g.store.GetGameserver(g.spec.ID); err == nil && dbGS != nil {
			dbGS.Installed = true
			g.store.UpdateGameserver(dbGS)
		}
	}
	if se.clearInstanceID {
		g.store.SetInstanceID(g.spec.ID, nil)
	}

	if se.publishOperationCleared {
		g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.spec.ID, &event.OperationData{Operation: nil}))
		g.notifyWatchers(nil)
	}
	if se.publishReady {
		g.bus.Publish(event.NewSystemEvent(event.EventGameserverReady, g.spec.ID, nil))
	}
	if se.publishInstanceExited {
		g.bus.Publish(event.NewSystemEvent(event.EventInstanceExited, g.spec.ID, nil))
	}
	if se.publishError != "" {
		g.bus.Publish(event.NewSystemEvent(event.EventGameserverError, g.spec.ID, &event.ErrorData{Reason: se.publishError}))
	}

	if se.crashLimitReached {
		g.log.Error("auto-restart limit reached", "gameserver", g.spec.ID, "crashes", se.crashCount, "max_restarts", 3)
	}
	if se.triggerAutoRestart {
		g.log.Warn("auto-restarting crashed gameserver", "gameserver", g.spec.ID, "attempt", se.crashCount, "max_restarts", 3)
		go func() {
			if err := g.Start(context.Background()); err != nil {
				g.log.Error("auto-restart failed", "gameserver", g.spec.ID, "error", err)
			}
		}()
	}
}

// setProcessRunningLocked records that the worker reports the process as alive.
// Caller must hold g.mu.
func (g *LiveGameserver) setProcessRunningLocked(update worker.InstanceStateUpdate) {
	g.processState = model.ProcessRunning
	g.ready = update.Ready
	startedAt := update.StartedAt
	g.startedAt = &startedAt
	if update.Ready && !update.ReadyAt.IsZero() {
		readyAt := update.ReadyAt
		g.readyAt = &readyAt
	}
	g.exitedAt = nil
	g.exitCode = 0
}

// setProcessExitedLocked records that the worker reports the process as terminated.
// Caller must hold g.mu.
func (g *LiveGameserver) setProcessExitedLocked(exitCode int, exitedAt time.Time) {
	g.processState = model.ProcessExited
	g.ready = false
	g.exitCode = exitCode
	if !exitedAt.IsZero() {
		g.exitedAt = &exitedAt
	} else {
		now := time.Now()
		g.exitedAt = &now
	}
	g.readyAt = nil
}

// clearProcessLocked resets observed process state. Called when the worker
// disconnects, the instance is removed, or we're archiving/stopping.
// Caller must hold g.mu.
func (g *LiveGameserver) clearProcessLocked() {
	g.processState = model.ProcessNone
	g.ready = false
	g.startedAt = nil
	g.readyAt = nil
	g.exitedAt = nil
	g.exitCode = 0
}

// SetWorker sets the worker implementation for this gameserver.
func (g *LiveGameserver) SetWorker(w worker.Worker) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.worker = w
}

// ClearWorker removes the worker and clears process state (worker went offline).
// Observed process facts are reset to ProcessNone because we can no longer
// see the worker — "we don't know what's happening" rather than lying about
// the last value we saw.
func (g *LiveGameserver) ClearWorker() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.worker = nil
	g.clearProcessLocked()
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
	// onto a different node, delete when the worker is offline) set this false.
	requireWorker bool
	// errorPrefix is prepended to error messages when the operation fails.
	// Empty means the operation is expected to set errors itself via setError.
	errorPrefix string
	// clearOnSuccess controls whether to clear the operation when fn returns nil.
	// Operations that end with the gameserver reaching "running" (Start, Restart,
	// Update, Reinstall, Migrate) leave the operation set so HandleProcessEvent
	// can clear it once the worker reports ready. Terminal operations that end
	// in a resting state without further signals (Stop, Archive, Unarchive) set
	// this true.
	clearOnSuccess bool
	// terminal marks operations that end with the gameserver object itself
	// going away (only Delete). On success we run onFinish to let the Manager
	// tear down the map entry and scripts dir, but we don't clearOperation —
	// the object is being removed, there is no operation to clear.
	terminal bool
	// onFinish runs after fn returns nil. For terminal ops, this is where the
	// Manager removes the live object and does its filesystem cleanup. Errors
	// are logged, not surfaced — the operation has already succeeded.
	onFinish func(context.Context) error
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
		return controller.ErrUnavailablef("worker unavailable for gameserver %s", g.spec.ID)
	}

	g.operation = &model.Operation{Type: opts.opType, Phase: opts.initialPhase}
	opCtx, cancel := context.WithCancel(context.Background())
	g.cancelOp = cancel
	g.opDone = make(chan struct{})
	g.mu.Unlock()

	// No install-time publish: the goroutine's fn is responsible for
	// publishing its phase via setPhase. The first setPhase (typically called
	// at the top of the execute function) announces the operation to watchers.

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
		if opts.onFinish != nil {
			if onErr := opts.onFinish(opCtx); onErr != nil {
				g.log.Error("operation onFinish hook failed", "type", opts.opType, "error", onErr)
			}
		}
		if opts.terminal {
			// The gameserver object is going away — skip clearOperation. No
			// subscriber should be looking at this object after onFinish ran.
			return
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
	if g.operation == nil {
		g.mu.Unlock()
		return
	}
	g.operation.Phase = phase
	op := g.operation
	g.mu.Unlock()

	g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.spec.ID, &event.OperationData{
		Operation: op,
	}))
	g.notifyWatchers(op)
}

// setProgress updates the current operation's progress and notifies watchers.
// Does not publish to the event bus — progress updates are high-frequency.
func (g *LiveGameserver) setProgress(progress model.OperationProgress) {
	g.mu.Lock()
	if g.operation == nil {
		g.mu.Unlock()
		return
	}
	g.operation.Progress = &progress
	op := g.operation
	g.mu.Unlock()

	g.notifyWatchers(op)
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
	g.operation = nil
	g.mu.Unlock()

	g.bus.Publish(event.NewSystemEvent(event.EventGameserverOperation, g.spec.ID, &event.OperationData{
		Operation: nil,
	}))
	g.notifyWatchers(nil)
}

// notifyWatchers sends the operation state to all watchers. Takes watcherMu
// itself, so it is safe to call with or without g.mu held. Non-blocking:
// drains and replaces if a consumer is behind.
func (g *LiveGameserver) notifyWatchers(op *model.Operation) {
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
// The game definition provides the container-side default ports that the game
// process binds to; the model ports have the allocated host-side ports.
func parseGameserverPorts(game *games.Game, gs *model.Gameserver) ([]worker.PortBinding, error) {
	bindings := make([]worker.PortBinding, 0, len(gs.Ports))
	for i, p := range gs.Ports {
		if int(p.Port) <= 0 {
			return nil, fmt.Errorf("invalid port mapping: port=%d", int(p.Port))
		}
		protocol := p.Protocol
		if protocol == "" {
			protocol = "tcp"
		}
		containerPort := int(p.Port)
		if i < len(game.DefaultPorts) {
			containerPort = game.DefaultPorts[i].Port
		}
		bindings = append(bindings, worker.PortBinding{
			Port:          int(p.Port),
			ContainerPort: containerPort,
			Protocol:      protocol,
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
