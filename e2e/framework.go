//go:build e2e

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
	"github.com/warsmite/gamejanitor/model"
	sdk "github.com/warsmite/gamejanitor/sdk"
)

// phase derives the one-word display summary from a gameserver snapshot's
// primary facts. The controller itself no longer produces a status enum —
// this helper exists so e2e assertions can read in a single check whether a
// gameserver is "running" / "stopped" / "error" / etc.
func phase(snap *sdk.Gameserver) string {
	if snap == nil {
		return ""
	}
	if snap.Operation != nil && snap.Operation.Phase == string(model.PhaseDeleting) {
		return "deleting"
	}
	if snap.DesiredState == string(model.DesiredArchived) {
		return "archived"
	}
	if !snap.WorkerOnline {
		return "unreachable"
	}
	if snap.Operation != nil {
		switch snap.Operation.Phase {
		case string(model.PhasePullingImage), string(model.PhaseDownloadingGame), string(model.PhaseInstalling):
			return "installing"
		case string(model.PhaseStopping):
			return "stopping"
		case string(model.PhaseStarting):
			return "starting"
		case string(model.PhaseMigrating):
			return "installing"
		}
	}
	if snap.ErrorReason != "" {
		return "error"
	}
	if snap.ProcessState == string(model.ProcessRunning) && snap.Ready {
		return "running"
	}
	return "stopped"
}

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

// RequireTestGame skips the current test unless it's running against the
// synthetic test-game. Used by tests that rely on TEST_BEHAVIOR injection
// (crash-after-ready, ignore-sigterm, etc.) which only the test-game
// supports. Real-game homelab runs skip these.
func (e *Env) RequireTestGame() {
	e.t.Helper()
	if e.GameID() != "test-game" {
		e.t.Skipf("requires test-game (TEST_BEHAVIOR injection), got %s", e.GameID())
	}
}

// OnlineWorkers returns the IDs of currently online workers.
func (e *Env) OnlineWorkers() []string {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	workers, err := e.sdk.Workers.List(ctx)
	require.NoError(e.t, err, "list workers")
	var ids []string
	for _, w := range workers {
		if w.Status == "online" {
			ids = append(ids, w.ID)
		}
	}
	return ids
}

// RequireMultiNode skips the test unless 2+ online workers are registered.
func (e *Env) RequireMultiNode() []string {
	e.t.Helper()
	workers := e.OnlineWorkers()
	if len(workers) < 2 {
		e.t.Skipf("requires 2+ online workers, got %d", len(workers))
	}
	return workers
}

// --- Auth / tokens ---

// Token is a handle to a created API token. Use SDK() to make authenticated
// API calls as this token.
type Token struct {
	env    *Env
	Raw    string
	ID     string
	Name   string
	Role   string
	client *sdk.Client
}

// NewToken creates a new API token using the env's default client (relies on
// localhost bypass). Use this to create the initial admin token before auth
// is enabled. After auth is enabled, use admin.NewToken() to create further
// tokens via the authed admin client.
func (e *Env) NewToken(req sdk.CreateTokenRequest) *Token {
	e.t.Helper()
	return e.newTokenVia(e.sdk, req)
}

// NewToken creates a new API token using this token's authenticated client.
// Admin tokens use this to create user/viewer tokens after auth is enabled.
func (t *Token) NewToken(req sdk.CreateTokenRequest) *Token {
	t.env.t.Helper()
	return t.env.newTokenVia(t.client, req)
}

func (e *Env) newTokenVia(c *sdk.Client, req sdk.CreateTokenRequest) *Token {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Tokens.Create(ctx, &req)
	require.NoError(e.t, err, "create token")
	return &Token{
		env:    e,
		Raw:    resp.Token,
		ID:     resp.TokenID,
		Name:   req.Name,
		Role:   req.Role,
		client: sdk.New(e.baseURL, sdk.WithToken(resp.Token)),
	}
}

// SDK returns an SDK client authenticated as this token.
func (t *Token) SDK() *sdk.Client { return t.client }

// SetSetting patches a single global setting. Uses the env's default client
// (anonymous if auth is off, admin-bypass on localhost).
func (e *Env) SetSetting(key string, value any) {
	e.t.Helper()
	e.setSettingVia(e.sdk, key, value)
}

// SetSettingAs patches a setting using a specific token's authed client.
// Needed once auth is enabled and localhost bypass is off.
func (e *Env) SetSettingAs(tok *Token, key string, value any) {
	e.t.Helper()
	e.setSettingVia(tok.client, key, value)
}

func (e *Env) setSettingVia(c *sdk.Client, key string, value any) {
	e.t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(e.t, err, "marshal setting %s", key)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = c.Settings.Update(ctx, map[string]json.RawMessage{key: raw})
	require.NoError(e.t, err, "set setting %s", key)
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

// Stop triggers the stop action. Stop returns 202 immediately; the actual
// teardown runs in the operation goroutine on the controller.
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

// Archive triggers the archive action.
func (gs *Gameserver) Archive() *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Archive(ctx, gs.id)
	require.NoError(gs.env.t, err, "archive %s", gs.id)
	return &Action{gs: gs, kind: "archive"}
}

// Unarchive triggers the unarchive action onto the given node (empty string
// for auto-placement).
func (gs *Gameserver) Unarchive(nodeID string) *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Unarchive(ctx, gs.id, nodeID)
	require.NoError(gs.env.t, err, "unarchive %s", gs.id)
	return &Action{gs: gs, kind: "unarchive"}
}

// Migrate triggers a migration to the given node.
func (gs *Gameserver) Migrate(targetNodeID string) *Action {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Migrate(ctx, gs.id, targetNodeID)
	require.NoError(gs.env.t, err, "migrate %s", gs.id)
	return &Action{gs: gs, kind: "migrate"}
}

// UpdateEnv patches the gameserver's env vars. Returns when the API call
// completes (synchronous metadata update).
func (gs *Gameserver) UpdateEnv(env map[string]string) {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.Update(ctx, gs.id, &sdk.UpdateGameserverRequest{Env: env})
	require.NoError(gs.env.t, err, "update env %s", gs.id)
}

// SendCommand sends a console command to the running gameserver.
func (gs *Gameserver) SendCommand(command string) {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gs.env.sdk.Gameservers.SendCommand(ctx, gs.id, command)
	require.NoError(gs.env.t, err, "send command to %s", gs.id)
}

// MustBeRunning waits until the gameserver reaches running state.
// Fails fast on gameserver.error. Fails on stall (no events for stallTimeout).
func (a *Action) MustBeRunning() { a.gs.env.t.Helper(); a.waitForReady() }

// MustBeStopped waits until the gameserver reaches stopped state.
func (a *Action) MustBeStopped() { a.gs.env.t.Helper(); a.waitForStopped() }

// MustBeGone waits until the gameserver is gone (delete completed).
func (a *Action) MustBeGone() { a.gs.env.t.Helper(); a.waitForGone() }

// MustBeError waits until the gameserver reaches error state and returns
// the error reason. Used for crash/install-failure tests.
func (a *Action) MustBeError() string {
	a.gs.env.t.Helper()
	return a.waitForError()
}

// MustReachOneOf waits for the gameserver's status to become any of the given
// values (via terminal events). Returns which one was reached. Uses stall
// detection. Useful for tests like "self-exit should reach error OR stopped".
func (a *Action) MustReachOneOf(statuses ...string) string {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	targetSet := map[string]bool{}
	for _, s := range statuses {
		targetSet[s] = true
	}

	// Fast path: already at target.
	if snap := a.gs.Snapshot(); snap != nil && snap.Operation == nil {
		if p := phase(snap); targetSet[p] {
			return p
		}
	}

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case _, ok := <-sub.ch:
			if !ok {
				a.gs.env.t.Fatalf("event stream closed while waiting for %v on %s", statuses, a.gs.id)
				return ""
			}
			resetTimer(stall, stallTimeout)

			// Re-read the snapshot on every event — primary signals changed,
			// so derive the phase from the current observed facts.
			if snap := a.gs.Snapshot(); snap != nil && snap.Operation == nil {
				if p := phase(snap); targetSet[p] {
					return p
				}
			}

		case <-stall.C:
			a.gs.env.t.Fatalf("stall: no events for %s while waiting for one of %v on %s\n%s",
				stallTimeout, statuses, a.gs.id, a.gs.env.dumpGameserver(a.gs.id))
			return ""
		}
	}
}

// waitForReady blocks until gameserver.ready fires for this gameserver.
func (a *Action) waitForReady() {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already running (no operation in flight).
	if snap := a.gs.Snapshot(); snap != nil && phase(snap) == "running" && snap.Operation == nil {
		return
	}

	a.waitForEventType(sub, "gameserver.ready", "running")
}

func (a *Action) waitForStopped() {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already stopped.
	if snap := a.gs.Snapshot(); snap != nil && phase(snap) == "stopped" && snap.Operation == nil {
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

// waitForError blocks until the gameserver transitions to error state.
// Triggered by gameserver.error events fired from handleUnexpectedDeath and
// setErrorLocked. Returns the reason.
func (a *Action) waitForError() string {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	// Fast path: already in error state.
	if snap := a.gs.Snapshot(); snap != nil && phase(snap) == "error" {
		return snap.ErrorReason
	}

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				a.gs.env.t.Fatalf("event stream closed while waiting for error on %s", a.gs.id)
				return ""
			}
			resetTimer(stall, stallTimeout)
			if ev.Type == "gameserver.error" {
				return jsonString(ev.Data, "reason")
			}
		case <-stall.C:
			a.gs.env.t.Fatalf("stall: no events for %s while waiting for error on %s\n%s",
				stallTimeout, a.gs.id, a.gs.env.dumpGameserver(a.gs.id))
			return ""
		}
	}
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
				if ph := jsonString(ev.Data, "operation.phase"); ph != "" {
					phases = append(phases, ph)
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

// --- Archive/unarchive waits ---

// MustBeArchived waits until the gameserver is archived (desired_state=archived,
// operation cleared).
func (a *Action) MustBeArchived() {
	a.gs.env.t.Helper()
	a.waitForSnapshot(func(s *sdk.Gameserver) bool {
		return s != nil && phase(s) == "archived" && s.Operation == nil
	}, "archived")
}

// MustMigrateTo waits for the gameserver to finish migrating to the target
// node. Verifies node_id changes AND operation clears. Whether the server
// auto-starts afterwards depends on its prior state (migration preserves it).
func (a *Action) MustMigrateTo(targetNodeID string) {
	a.gs.env.t.Helper()
	a.waitForSnapshot(func(s *sdk.Gameserver) bool {
		return s != nil && s.NodeID != nil && *s.NodeID == targetNodeID && s.Operation == nil
	}, "migrated to "+targetNodeID)
}

// MustBeUnarchived waits until the gameserver is unarchived (status=stopped,
// desired_state=stopped, operation cleared).
func (a *Action) MustBeUnarchived() {
	a.gs.env.t.Helper()
	a.waitForSnapshot(func(s *sdk.Gameserver) bool {
		return s != nil && s.DesiredState == "stopped" && phase(s) == "stopped" && s.Operation == nil
	}, "unarchived")
}

// waitForSnapshot polls via events but checks state by fetching the snapshot
// after each event. Use for compound state conditions (status + desired_state
// + no operation) that aren't captured by a single terminal event.
func (a *Action) waitForSnapshot(match func(*sdk.Gameserver) bool, label string) {
	a.gs.env.t.Helper()

	sub := a.gs.env.stream.subscribe(a.gs.id)
	defer sub.close()

	if snap := a.gs.Snapshot(); match(snap) {
		return
	}

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case _, ok := <-sub.ch:
			if !ok {
				a.gs.env.t.Fatalf("event stream closed while waiting for %s on %s", label, a.gs.id)
				return
			}
			resetTimer(stall, stallTimeout)
			if match(a.gs.Snapshot()) {
				return
			}
		case <-stall.C:
			a.gs.env.t.Fatalf("stall: no events for %s while waiting for %s on %s\n%s",
				stallTimeout, label, a.gs.id, a.gs.env.dumpGameserver(a.gs.id))
			return
		}
	}
}

// --- File operations ---

// WriteFile writes content to a path on the gameserver volume.
func (gs *Gameserver) WriteFile(path string, content []byte) {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := gs.env.sdk.Files.Write(ctx, gs.id, path, content)
	require.NoError(gs.env.t, err, "write %s to %s", path, gs.id)
}

// ReadFile reads a file from the gameserver volume.
func (gs *Gameserver) ReadFile(path string) string {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	content, err := gs.env.sdk.Files.Read(ctx, gs.id, path)
	require.NoError(gs.env.t, err, "read %s from %s", path, gs.id)
	return content
}

// ListFiles lists entries in a directory on the gameserver volume.
func (gs *Gameserver) ListFiles(path string) []sdk.FileEntry {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entries, err := gs.env.sdk.Files.List(ctx, gs.id, path)
	require.NoError(gs.env.t, err, "list files at %s on %s", path, gs.id)
	return entries
}

// --- Backup flow ---

// Backup is a handle to a backup initiated on a gameserver. Call
// MustComplete() to block until the backup finishes.
type Backup struct {
	gs *Gameserver
	id string
}

// BackupNow triggers a backup and returns a handle to wait on.
func (gs *Gameserver) BackupNow(name string) *Backup {
	gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	b, err := gs.env.sdk.Backups.Create(ctx, gs.id, &sdk.CreateBackupRequest{Name: name})
	require.NoError(gs.env.t, err, "create backup on %s", gs.id)
	require.NotEmpty(gs.env.t, b.ID, "backup ID")
	return &Backup{gs: gs, id: b.ID}
}

// ID returns the backup ID.
func (b *Backup) ID() string { return b.id }

// MustComplete blocks until the backup status reaches "completed", or fails
// fast on "failed" / stall.
func (b *Backup) MustComplete() {
	b.gs.env.t.Helper()

	sub := b.gs.env.stream.subscribe(b.gs.id)
	defer sub.close()

	// Fast path: poll once to see if already done.
	if status := b.currentStatus(); status == "completed" {
		return
	} else if status == "failed" {
		b.gs.env.t.Fatalf("backup %s failed immediately on %s", b.id, b.gs.id)
		return
	}

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				b.gs.env.t.Fatalf("event stream closed while waiting for backup %s", b.id)
				return
			}
			resetTimer(stall, stallTimeout)
			if ev.Type == "backup.completed" && jsonString(ev.Data, "backup.id") == b.id {
				return
			}
			if ev.Type == "backup.failed" && jsonString(ev.Data, "backup.id") == b.id {
				b.gs.env.t.Fatalf("backup %s failed: %s", b.id, jsonString(ev.Data, "error"))
				return
			}
		case <-stall.C:
			b.gs.env.t.Fatalf("stall: no events for %s while waiting for backup %s to complete",
				stallTimeout, b.id)
			return
		}
	}
}

// Restore restores this backup onto its source gameserver. Returns when the
// restore completes (observed by a write-then-read to a marker file is the
// typical pattern, not a single event). Callers should verify data was
// restored by reading expected file content.
// Restore triggers a restore of this backup onto its source gameserver and
// returns a handle to wait on.
func (b *Backup) Restore() *Restore {
	b.gs.env.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := b.gs.env.sdk.Backups.Restore(ctx, b.gs.id, b.id)
	require.NoError(b.gs.env.t, err, "restore backup %s", b.id)
	return &Restore{gs: b.gs, backupID: b.id}
}

// Restore is a handle to an in-flight restore operation.
type Restore struct {
	gs       *Gameserver
	backupID string
}

// MustComplete blocks until the restore completes, or fails fast on a
// restore.failed event / stall.
func (r *Restore) MustComplete() {
	r.gs.env.t.Helper()

	sub := r.gs.env.stream.subscribe(r.gs.id)
	defer sub.close()

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				r.gs.env.t.Fatalf("event stream closed while waiting for restore %s", r.backupID)
				return
			}
			resetTimer(stall, stallTimeout)
			if ev.Type == "backup.restore.completed" && jsonString(ev.Data, "backup.id") == r.backupID {
				return
			}
			if ev.Type == "backup.restore.failed" && jsonString(ev.Data, "backup.id") == r.backupID {
				r.gs.env.t.Fatalf("restore %s failed: %s", r.backupID, jsonString(ev.Data, "error"))
				return
			}
		case <-stall.C:
			r.gs.env.t.Fatalf("stall: no events for %s while waiting for restore %s", stallTimeout, r.backupID)
			return
		}
	}
}

// currentStatus fetches the backup's current status via list. Returns ""
// if not found.
func (b *Backup) currentStatus() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	backups, err := b.gs.env.sdk.Backups.List(ctx, b.gs.id, nil)
	if err != nil {
		return ""
	}
	for _, bk := range backups {
		if bk.ID == b.id {
			return bk.Status
		}
	}
	return ""
}

// --- Event subscription (raw) ---

// WaitForEvent blocks until an event of the given type fires for this
// gameserver. Stall-protected. Returns the event's raw data payload.
func (gs *Gameserver) WaitForEvent(eventType string) json.RawMessage {
	gs.env.t.Helper()

	sub := gs.env.stream.subscribe(gs.id)
	defer sub.close()

	stall := time.NewTimer(stallTimeout)
	defer stall.Stop()

	for {
		select {
		case ev, ok := <-sub.ch:
			if !ok {
				gs.env.t.Fatalf("event stream closed while waiting for %q on %s", eventType, gs.id)
				return nil
			}
			resetTimer(stall, stallTimeout)
			if ev.Type == eventType {
				return ev.Data
			}
		case <-stall.C:
			gs.env.t.Fatalf("stall: no events for %s while waiting for %q on %s",
				stallTimeout, eventType, gs.id)
			return nil
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

// contextWithTimeout is a short helper for tests that need a context with
// a deadline for a single SDK call.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

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
