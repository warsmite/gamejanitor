package worker

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"regexp"
	"sync"
	"time"
)

// InstanceTracker maintains authoritative instance state on the worker side.
// Both sandbox and Docker runtimes embed this to get consistent state management,
// ready pattern detection, and state streaming.
type InstanceTracker struct {
	mu        sync.Mutex
	instances map[string]*TrackedInstance
	ch        chan InstanceStateUpdate
	log       *slog.Logger
}

type TrackedInstance struct {
	ID           string
	Name         string
	State        InstanceState
	ExitCode     int
	StartedAt    time.Time
	ExitedAt     time.Time
	Installed    bool
	readyPattern *regexp.Regexp
	cancel       context.CancelFunc // cancels the log watcher goroutine
}

func NewInstanceTracker(log *slog.Logger) *InstanceTracker {
	return &InstanceTracker{
		instances: make(map[string]*TrackedInstance),
		ch:        make(chan InstanceStateUpdate, 64),
		log:       log,
	}
}

// Track registers an instance in the tracker with initial state Created.
func (t *InstanceTracker) Track(id, name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.instances[id] = &TrackedInstance{
		ID:   id,
		Name: name,
	}
}

// SetState transitions an instance to the given state and emits an update.
func (t *InstanceTracker) SetState(id string, state InstanceState) {
	t.mu.Lock()
	inst, ok := t.instances[id]
	if !ok {
		t.mu.Unlock()
		return
	}

	old := inst.State
	inst.State = state

	switch state {
	case StateStarting:
		inst.StartedAt = time.Now()
	case StateExited:
		inst.ExitedAt = time.Now()
		if inst.cancel != nil {
			inst.cancel()
			inst.cancel = nil
		}
	}

	update := t.snapshotLocked(inst)
	t.mu.Unlock()

	t.log.Info("instance state transition", "id", id, "from", old, "to", state)
	t.emit(update)
}

// SetExited transitions to exited with a specific exit code.
func (t *InstanceTracker) SetExited(id string, exitCode int) {
	t.mu.Lock()
	inst, ok := t.instances[id]
	if !ok {
		t.mu.Unlock()
		return
	}

	inst.State = StateExited
	inst.ExitCode = exitCode
	inst.ExitedAt = time.Now()
	if inst.cancel != nil {
		inst.cancel()
		inst.cancel = nil
	}

	update := t.snapshotLocked(inst)
	t.mu.Unlock()

	t.log.Info("instance exited", "id", id, "exit_code", exitCode)
	t.emit(update)
}

// SetInstalled marks the instance as having completed installation.
func (t *InstanceTracker) SetInstalled(id string) {
	t.mu.Lock()
	inst, ok := t.instances[id]
	if !ok {
		t.mu.Unlock()
		return
	}
	inst.Installed = true
	update := t.snapshotLocked(inst)
	t.mu.Unlock()

	t.emit(update)
}

// Remove removes an instance from tracking.
func (t *InstanceTracker) Remove(id string) {
	t.mu.Lock()
	inst, ok := t.instances[id]
	if ok {
		if inst.cancel != nil {
			inst.cancel()
		}
		delete(t.instances, id)
	}
	t.mu.Unlock()
}

// Get returns the current state of a tracked instance, or nil if not found.
func (t *InstanceTracker) Get(id string) *InstanceStateUpdate {
	t.mu.Lock()
	defer t.mu.Unlock()
	inst, ok := t.instances[id]
	if !ok {
		return nil
	}
	update := t.snapshotLocked(inst)
	return &update
}

// Snapshot returns the current state of all tracked instances.
func (t *InstanceTracker) Snapshot() []InstanceStateUpdate {
	t.mu.Lock()
	defer t.mu.Unlock()
	updates := make([]InstanceStateUpdate, 0, len(t.instances))
	for _, inst := range t.instances {
		updates = append(updates, t.snapshotLocked(inst))
	}
	return updates
}

// Events returns the channel that receives state updates.
// Consumed by the gRPC agent to stream to the controller.
func (t *InstanceTracker) Events() <-chan InstanceStateUpdate {
	return t.ch
}

// WatchLogs starts a goroutine that scans instance logs for the ready pattern.
// When matched, the instance is promoted from Starting to Running.
// If readyPattern is empty, promotes immediately.
func (t *InstanceTracker) WatchLogs(ctx context.Context, id string, readyPattern string, logReader io.ReadCloser) {
	t.mu.Lock()
	inst, ok := t.instances[id]
	if !ok {
		t.mu.Unlock()
		logReader.Close()
		return
	}

	if readyPattern == "" {
		inst.State = StateRunning
		update := t.snapshotLocked(inst)
		t.mu.Unlock()
		logReader.Close()
		t.log.Info("no ready pattern, promoting immediately", "id", id)
		t.emit(update)
		return
	}

	re, err := regexp.Compile(readyPattern)
	if err != nil {
		t.mu.Unlock()
		logReader.Close()
		t.log.Error("invalid ready pattern, promoting immediately", "id", id, "pattern", readyPattern, "error", err)
		t.SetState(id, StateRunning)
		return
	}

	inst.readyPattern = re
	watchCtx, cancel := context.WithCancel(ctx)
	inst.cancel = cancel
	t.mu.Unlock()

	go t.watchLogsLoop(watchCtx, id, re, logReader)
}

// Recover re-registers an instance that survived a worker restart.
// Used by sandbox recovery to re-add instances to the tracker without
// emitting state transitions (the controller will get these via GetAllInstanceStates).
func (t *InstanceTracker) Recover(id, name string, state InstanceState, startedAt time.Time, installed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.instances[id] = &TrackedInstance{
		ID:        id,
		Name:      name,
		State:     state,
		StartedAt: startedAt,
		Installed: installed,
	}
}

func (t *InstanceTracker) watchLogsLoop(ctx context.Context, id string, re *regexp.Regexp, logReader io.ReadCloser) {
	defer logReader.Close()
	scanner := bufio.NewScanner(logReader)
	// Increase buffer for games with very long log lines
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		if re.MatchString(line) {
			t.log.Info("ready pattern matched", "id", id)
			t.SetState(id, StateRunning)
			return
		}
	}
}

func (t *InstanceTracker) snapshotLocked(inst *TrackedInstance) InstanceStateUpdate {
	return InstanceStateUpdate{
		InstanceID:   inst.ID,
		InstanceName: inst.Name,
		State:        inst.State,
		ExitCode:     inst.ExitCode,
		StartedAt:    inst.StartedAt,
		ExitedAt:     inst.ExitedAt,
		Installed:    inst.Installed,
	}
}

func (t *InstanceTracker) emit(update InstanceStateUpdate) {
	select {
	case t.ch <- update:
	default:
		t.log.Warn("instance state update dropped (channel full)", "id", update.InstanceID, "state", update.State)
	}
}
