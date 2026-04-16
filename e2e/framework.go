//go:build e2e || smoke

package e2e

// New e2e framework. Waits are event-driven, not time-bound. Per-action
// timeouts are replaced with a stall detector: a wait blocks until its
// target condition fires OR the event stream goes silent for stallTimeout.
// CI supplies the outer deadline via `go test -timeout <X>`.
//
// Tests use the SDK (github.com/warsmite/gamejanitor/sdk) for all API calls.
// No direct HTTP client code in test bodies. The SDK's type definitions
// become the test contract; when the controller changes the API shape,
// the SDK updates first and tests follow.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdk "github.com/warsmite/gamejanitor/sdk"
)

// stallTimeout is how long a wait tolerates silence on the event stream
// before failing. Must be generous enough for the quietest legitimate
// phase. Truly broken systems fail within this window; healthy systems
// reset the timer on every event.
const stallTimeout = 120 * time.Second

// Env is the test environment handle. Each test gets one via e2e.Start(t).
type Env struct {
	t       *testing.T
	baseURL string
	sdk     *sdk.Client
	stream  *eventRouter
}

// NewEnv returns an Env for parallel-safe tests.
func NewEnv(t *testing.T) *Env {
	t.Helper()
	t.Parallel()
	return newEnv(t)
}

// NewEnvSerial is for tests that mutate global state (auth settings, etc.)
// and must not run concurrently.
func NewEnvSerial(t *testing.T) *Env {
	t.Helper()
	return newEnv(t)
}

func newEnv(t *testing.T) *Env {
	url := os.Getenv("GAMEJANITOR_API_URL")
	if url == "" {
		localOnce.Do(func() { startLocalInstance(t) })
		url = localURL
	}

	client := sdk.New(url)
	e := &Env{t: t, baseURL: url, sdk: client}
	e.waitForControllerReady()
	e.waitForWorker()
	e.stream = newEventRouter(t, client)
	t.Cleanup(e.stream.stop)
	return e
}

// SDK returns the underlying SDK client for tests that need direct API
// access. Prefer the Env and Gameserver helpers where possible.
func (e *Env) SDK() *sdk.Client { return e.sdk }

// BaseURL returns the controller's base URL. For tests that need to make
// unusual HTTP calls (file upload/download, etc.).
func (e *Env) BaseURL() string { return e.baseURL }

// GameID returns the game this test run targets.
func (e *Env) GameID() string {
	if id := os.Getenv("E2E_GAME_ID"); id != "" {
		return id
	}
	return "test-game"
}

// GameEnv returns the environment variables required by the target game.
func (e *Env) GameEnv() map[string]string {
	switch e.GameID() {
	case "minecraft-java":
		return map[string]string{"EULA": "true", "MINECRAFT_VERSION": "1.21.4"}
	default:
		return map[string]string{"REQUIRED_VAR": "yes"}
	}
}

// --- Controller readiness ---

func (e *Env) waitForControllerReady() {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(e.baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("controller not ready within 30s at %s", e.baseURL)
}

func (e *Env) waitForWorker() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		workers, err := e.sdk.Workers.List(ctx)
		if err == nil && len(workers) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			e.t.Fatalf("no workers within 30s: %v", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// --- Gameserver handle ---

// Gameserver is a handle to a created gameserver. Action methods (Start,
// Stop, etc.) return an *Action — call one of its Must* methods to block
// until the outcome is observed.
type Gameserver struct {
	env *Env
	id  string
}

// NewGameserver creates a gameserver with the env's game and default env
// vars, and registers cleanup. Pass a map to override individual fields.
func (e *Env) NewGameserver(overrides ...map[string]any) *Gameserver {
	e.t.Helper()

	req := &sdk.CreateGameserverRequest{
		Name:   e.t.Name(),
		GameID: e.GameID(),
		Env:    e.GameEnv(),
	}
	// Overrides as raw JSON: marshal the req, merge overrides, unmarshal back.
	// This keeps the call site ergonomic for ad-hoc fields like "auto_restart".
	if len(overrides) > 0 {
		raw, _ := json.Marshal(req)
		var merged map[string]any
		_ = json.Unmarshal(raw, &merged)
		for _, o := range overrides {
			for k, v := range o {
				merged[k] = v
			}
		}
		raw, _ = json.Marshal(merged)
		req = &sdk.CreateGameserverRequest{}
		_ = json.Unmarshal(raw, req)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	resp, err := e.sdk.Gameservers.Create(ctx, req)
	require.NoError(e.t, err, "create gameserver")
	require.NotEmpty(e.t, resp.ID, "gameserver ID")

	gs := &Gameserver{env: e, id: resp.ID}
	e.t.Cleanup(gs.teardown)
	return gs
}

// ID returns the gameserver's unique ID.
func (gs *Gameserver) ID() string { return gs.id }

// Snapshot fetches the current gameserver state. Returns nil if the
// gameserver doesn't exist (deleted).
func (gs *Gameserver) Snapshot() *sdk.Gameserver {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := gs.env.sdk.Gameservers.Get(ctx, gs.id)
	if err != nil {
		if sdk.IsNotFound(err) {
			return nil
		}
		gs.env.t.Fatalf("snapshot %s: %v", gs.id, err)
	}
	return s
}

// teardown is best-effort cleanup on test end.
func (gs *Gameserver) teardown() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = gs.env.sdk.Gameservers.Stop(ctx, gs.id)
	_ = gs.env.sdk.Gameservers.Delete(ctx, gs.id)
}

// --- Actions ---

// Action is returned by a mutating call on Gameserver. Call one of its
// Must* methods to block until the expected outcome occurs.
type Action struct {
	gs   *Gameserver
	kind string
}

// Start triggers the start action.
func (gs *Gameserver) Start() *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Start(ctx, gs.id)
	require.NoError(gs.env.t, err, "start %s", gs.id)
	return &Action{gs: gs, kind: "start"}
}

// Stop triggers the stop action.
func (gs *Gameserver) Stop() *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Stop(ctx, gs.id)
	require.NoError(gs.env.t, err, "stop %s", gs.id)
	return &Action{gs: gs, kind: "stop"}
}

// Restart triggers the restart action.
func (gs *Gameserver) Restart() *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Restart(ctx, gs.id)
	require.NoError(gs.env.t, err, "restart %s", gs.id)
	return &Action{gs: gs, kind: "restart"}
}

// Delete triggers the delete action.
func (gs *Gameserver) Delete() *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := gs.env.sdk.Gameservers.Delete(ctx, gs.id)
	require.NoError(gs.env.t, err, "delete %s", gs.id)
	return &Action{gs: gs, kind: "delete"}
}

// MustBeRunning waits until the gameserver reaches running state.
// Fails fast on gameserver.error. Fails on stall (no events for stallTimeout).
func (a *Action) MustBeRunning() { a.gs.env.t.Helper(); a.waitForReady() }

// MustBeStopped waits until the gameserver reaches stopped state.
func (a *Action) MustBeStopped() { a.gs.env.t.Helper(); a.waitForStopped() }

// MustBeGone waits until the gameserver is gone (delete completed).
func (a *Action) MustBeGone() { a.gs.env.t.Helper(); a.waitForGone() }

// waitForReady blocks until gameserver.ready fires for this gameserver.
func (a *Action) waitForReady() {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already running (no operation in flight).
	if snap := a.gs.Snapshot(); snap != nil && snap.Status == "running" && snap.Operation == nil {
		return
	}

	a.waitForEventType(sub, "gameserver.ready", "running")
}

func (a *Action) waitForStopped() {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already stopped.
	if snap := a.gs.Snapshot(); snap != nil && snap.Status == "stopped" && snap.Operation == nil {
		return
	}

	a.waitForEventType(sub, "gameserver.instance_stopped", "stopped")
}

func (a *Action) waitForGone() {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already gone.
	if a.gs.Snapshot() == nil {
		return
	}

	a.waitForEventType(sub, "gameserver.delete", "gone")
}

// waitForEventType is the core wait primitive. Blocks until the given
// event type fires for the subscribed gameserver, or the stream goes
// silent for stallTimeout, or an error event arrives (fail fast).
// targetLabel is used only in error messages for clarity.
func (a *Action) waitForEventType(sub *subscription, target string, targetLabel string) {
	a.gs.env.t.Helper()

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	var phases []string

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				a.gs.env.t.Fatalf("event stream closed while waiting for %s on %s", targetLabel, a.gs.id)
				return
			}
			resetTimer(stall, stallTimeout)

			// Track phase history for diagnostics.
			if ev.Type == "gameserver.operation" {
				if phase := jsonString(ev.Data, "operation.phase"); phase != "" {
					phases = append(phases, phase)
				}
			}

			if ev.Type == target {
				return
			}

			// Fail-fast signals: gameserver.error is terminal for "running" target.
			if targetLabel == "running" && ev.Type == "gameserver.error" {
				a.gs.env.t.Fatalf("gameserver %s errored while waiting for running: %s\n  phases: %v\n%s",
					a.gs.id, jsonString(ev.Data, "reason"), phases, a.gs.env.dumpGameserver(a.gs.id))
				return
			}

		case <-stall.C:
			a.gs.env.t.Fatalf("stall: no events for %s while waiting for %s on %s\n  phases: %v\n%s",
				stallTimeout, targetLabel, a.gs.id, phases, a.gs.env.dumpGameserver(a.gs.id))
			return
		}
	}
}

// --- Event router ---

// eventRouter subscribes to the SDK's event stream and fans out events to
// per-gameserver subscribers. Each subscription has a small buffer;
// overflows are dropped (tests should read events as they come).
type eventRouter struct {
	mu          sync.Mutex
	subscribers map[string][]*subscription
	cancel      context.CancelFunc
	done        chan struct{}
}

type subscription struct {
	gsID string
	ch   chan routedEvent
	r    *eventRouter
}

type routedEvent struct {
	Type string
	Data json.RawMessage
}

func newEventRouter(t *testing.T, client *sdk.Client) *eventRouter {
	ctx, cancel := context.WithCancel(context.Background())
	r := &eventRouter{
		subscribers: make(map[string][]*subscription),
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	events, err := client.Events.Subscribe(ctx)
	require.NoError(t, err, "subscribe to event stream")

	go r.run(events)
	return r
}

func (r *eventRouter) run(events <-chan sdk.SSEEvent) {
	defer close(r.done)
	for ev := range events {
		gsID := jsonString(ev.Data, "gameserver_id")
		if gsID == "" {
			continue
		}
		r.dispatch(gsID, routedEvent{Type: ev.Type, Data: ev.Data})
	}
}

func (r *eventRouter) dispatch(gsID string, ev routedEvent) {
	r.mu.Lock()
	subs := append([]*subscription(nil), r.subscribers[gsID]...)
	r.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- ev:
		default:
			// Buffer full — drop. Tests should read as they come.
		}
	}
}

func (r *eventRouter) subscribe(gsID string) *subscription {
	s := &subscription{gsID: gsID, ch: make(chan routedEvent, 32), r: r}
	r.mu.Lock()
	r.subscribers[gsID] = append(r.subscribers[gsID], s)
	r.mu.Unlock()
	return s
}

func (s *subscription) close() {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	list := s.r.subscribers[s.gsID]
	for i, sub := range list {
		if sub == s {
			s.r.subscribers[s.gsID] = append(list[:i], list[i+1:]...)
			break
		}
	}
}

func (r *eventRouter) stop() {
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(time.Second):
	}
}

// --- Utilities ---

func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}

// jsonString extracts a string value from raw JSON. Supports dotted paths
// for nested objects (e.g. "operation.phase"). Returns "" if missing.
func jsonString(raw json.RawMessage, path string) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	cur := v
	for _, part := range splitPath(path) {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[part]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

func splitPath(p string) []string {
	var out []string
	start := 0
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			out = append(out, p[start:i])
			start = i + 1
		}
	}
	out = append(out, p[start:])
	return out
}

// dumpGameserver returns a readable snapshot of the gameserver for failure
// messages. Includes the full state the API returns.
func (e *Env) dumpGameserver(gsID string) string {
	snap, err := e.sdk.Gameservers.Get(context.Background(), gsID)
	if err != nil {
		return fmt.Sprintf("(dump failed: %v)", err)
	}
	b, _ := json.MarshalIndent(snap, "  ", "  ")
	return "  gameserver state: " + string(b)
}
