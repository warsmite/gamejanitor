# Testing Strategy

## Overview

Gamejanitor has ~22K lines of Go with zero test coverage. This document outlines the strategy for building a test suite that catches real bugs, focusing on business logic correctness, permission enforcement, multi-node orchestration, and data integrity.

## Principles

- **Test behavior, not implementation.** Tests assert on observable outcomes (DB state, HTTP responses, events published), not internal function calls.
- **Real DB, fake workers.** Every test uses a real SQLite database with real migrations applied. Container runtimes are faked at the `Worker` interface boundary.
- **Tests read like specs.** Each test describes a scenario: given this state, when this happens, expect this outcome.
- **Integration over mocking.** The only mock is the `Worker` interface (the runtime boundary). Everything above it — services, event bus, auth, middleware — runs for real.
- **Fast by default, Docker opt-in.** Most tests run without Docker. Worker-layer tests that need a real container runtime use `//go:build integration`.

## Conventions

### Naming
Tests use the pattern `TestServiceName_MethodOrBehavior_Scenario`:
- `TestGameserver_Create_MissingRequiredEnvVar`
- `TestAuth_ValidateToken_ExpiredTokenRejected`
- `TestPortAllocation_ConcurrentCreates_NoDuplicatePorts`

Verbose, but grep-friendly and reads clearly in `go test -v` output.

### Isolation
Each test gets its own in-memory SQLite database with fresh migrations applied. No shared state between tests, no cleanup needed. Tests run in parallel (`t.Parallel()`) by default — the per-test DB guarantees no cross-contamination.

### Event Timing
The event bus is async (buffered channels). Tests that assert "event X was published after operation Y" use the `WaitForEvent(t, bus, eventType, timeout)` helper with a 2-second default timeout. Tests that don't care about events just ignore them. This avoids flaky timing issues while keeping tests fast.

### Bug Handling During Test Development

Tests will inevitably expose real bugs. How we handle them depends on the type:

**Obvious small bugs** (typo in a query, off-by-one, missing nil check) — fix inline while writing the test. The test proves the fix.

**Real design issues** (e.g. memory_limit_mb=0 bug, permission enforcement gaps, race conditions) — these are the whole point. Write the test asserting *correct* behavior, skip it with an explanation:

```go
func TestZeroMemoryMeansUnlimited(t *testing.T) {
    t.Skip("BUG: memory_limit_mb=0 gets overridden by applyGameDefaults — see MEMORY.md")
    // ... test that asserts the correct behavior
}
```

This keeps `go test ./...` green while building a backlog of reproducible bug reports. Every skipped bug test must also be logged in `TESTING_BUGS.md` (see below).

### Bug Tracker: `TESTING_BUGS.md`

All bugs discovered during test development are documented in `TESTING_BUGS.md` at the project root. Each entry includes:
- Short description
- Skipped test name and file location
- Expected vs actual behavior
- Severity estimate (blocks release / should fix / cosmetic)

The skip message in code references `TESTING_BUGS.md`, and the doc entry references the test file — both directions linked. This prevents bugs from getting lost in skip messages scattered across test files.

`TESTING_BUGS.md` also tracks **API surface issues** — things that aren't bugs but caused confusion during test development (e.g., inconsistent return signatures, misleading defaults, naming mismatches). These signal interfaces that could be improved and are worth addressing before they confuse real integrators.

**Foundation issues** (fake worker mismatch, service wiring wrong) — fix immediately in Phase 2. That's the validation phase's purpose.

**Ambiguous behavior** (code does something, unclear if intentional) — write the test asserting current behavior, add a comment: `// NOTE: current behavior — is this intentional?`

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/stretchr/testify` | Assertions (`require`/`assert`) and suite support |
| Standard `testing`, `net/http/httptest` | Test runner and HTTP testing |

## Test Layers

### Tier 1: Models (`models/*_test.go`)

Tests for the data access layer against a real in-memory SQLite database.

**What to test:**
- CRUD operations for all models (gameservers, tokens, schedules, backups, etc.)
- Allocation queries (`AllocatedMemoryByNode`, `AllocatedCPUByNode`, `AllocatedStorageByNode`)
- Filtering and pagination
- JSON column handling (ports, env, tags, permissions, gameserver_ids)
- Foreign key cascades (delete gameserver cascades schedules, backups, mods)
- Unique constraints and conflict behavior
- Edge cases: empty JSON arrays, null vs zero values, very long strings

**Not tested here:** Business logic, validation, authorization.

### Tier 2: Service (`service/*_test.go`)

The bulk of the test suite. Tests business logic with real DB + fake Worker + real event bus.

**Subsystems and focus areas:**

#### Gameserver Lifecycle
- Create with valid/invalid game ID, missing required env vars, unknown fields
- Start → ready → stop → delete happy path with event assertions
- Restart (stop + start), update while running vs stopped
- Delete cleans up container, volume, schedules, backups, mods
- Status transitions: verify correct status at each stage
- Error states: start failure mid-pull, container crash, stop timeout

#### Multi-Node Placement
- `RankWorkersForPlacement` scoring: memory/CPU/storage headroom
- Tag filtering: required tags must all match
- Cordoned workers excluded from placement
- Capacity overflow: reject when node is full
- Placement with no available workers returns error
- Concurrent creates don't double-allocate ports (goroutine race test)

#### Port Allocation
- Contiguous block allocation from port range
- Multiple gameservers fill the range correctly
- Port exhaustion returns clear error
- Per-worker port ranges respected in multi-node
- Ports freed on gameserver delete, reusable by next create

#### Migration
- Happy path: stop → backup → transfer → restore → reallocate ports → update DB
- Target node must have capacity and required tags
- Source and target workers must be online
- Failure during transfer: source data preserved
- Auto-migration triggered by resource update exceeding node capacity

#### Resource Enforcement
- Create rejected when memory/CPU/storage exceeds node limits
- Update rejected when new resources exceed node limits (unless auto-migrate)
- Zero limits treated correctly (unlimited vs misconfigured — see memory_limit_mb=0 bug)
- Node without explicit limits: behavior documented and tested

#### Auth & Permissions
- Admin token bypasses all checks
- Custom token with gameserver_ids scoping: can only access listed gameservers
- Custom token with empty gameserver_ids: access all gameservers
- Permission check: token must have specific permission for operation
- Expired token rejected
- Non-admin token blocked from changing resources/placement on update
- Worker token can only do worker operations

#### Input Validation
- Gameserver name: empty, too long, special characters
- Environment variables: required vars missing, wrong types
- Port mappings: invalid protocol, out of range
- Cron expressions: malformed syntax
- File paths: traversal attempts (`../../etc/passwd`)
- Backup names: allowed characters, duplicates

#### Backups
- Create backup records status progression (in_progress → completed)
- Retention enforcement: oldest deleted when limit reached
- Per-gameserver backup_limit overrides global setting
- Restore flow: stops gameserver, wipes volume, extracts backup
- Restore failure: gameserver left in error state (no rollback)

#### Schedules
- CRUD with valid/invalid cron expressions
- Task execution: restart, backup, command types
- One-shot: disabled after first execution
- Schedule for deleted gameserver: cascade delete

#### Events
- Correct event types published for each lifecycle operation
- Actor tracking: API token vs scheduler vs system
- Status events derived from lifecycle events (StatusSubscriber)
- Event persistence (EventStoreSubscriber)
- High-frequency events (stats, query) NOT persisted

#### Webhooks
- Event filter matching: `*`, `gameserver.*`, `gameserver.started`
- HMAC-SHA256 signature correctness
- Retry backoff: exponential with max
- Delivery state transitions: pending → delivered or pending → failed
- Disabled endpoint skipped

#### Settings
- Defaults applied when no DB value exists
- DB value overrides default
- Config file values written to DB on startup
- Type coercion (string → int, string → bool)

### Tier 3: API (`api/handlers/*_test.go`)

HTTP-level tests using `httptest.Server` with the full chi router and middleware chain.

**What to test:**
- **Auth middleware:** missing token → 401, expired → 401, wrong scope → 403, auth disabled → pass
- **Permission enforcement per endpoint:** verify every endpoint rejects unauthorized tokens
- **Input validation:** malformed JSON → 400, missing required fields → 400, invalid values → 400
- **Response contract:** all responses use `{"status": "ok/error", ...}` envelope
- **Status codes:** 200, 201, 204, 400, 401, 403, 404, 409, 500 used correctly
- **Rate limiting:** verify rate limit headers, burst behavior
- **CORS/security headers:** present on all responses
- **Pagination:** offset/limit parameters, default behavior

**Not tested here:** Business logic edge cases (covered in Tier 2).

### Tier 4: Worker Integration (`worker/*_test.go`)

Requires Docker. Build-tagged with `//go:build integration`.

**What to test:**
- Container lifecycle: pull Alpine, create, start, inspect, stop, remove
- Volume operations: create, write file, read file, list files, delete
- Direct access vs sidecar detection and fallback
- Backup volume → restore volume round-trip (data integrity)
- File path traversal prevention at the worker level
- Log parsing: Docker multiplexed format and raw format
- Container stats collection

**What NOT to test here:** Game-specific behavior, business logic (that's Tier 2).

### Game Definition Validation (`games/*_test.go`)

Small, focused tests using real game definitions from `games/data/`.

**What to test:**
- All game YAMLs parse without error
- Required fields present (id, name, base_image, ports)
- No port conflicts within a game definition
- Ready patterns compile as valid regex
- Env var types are valid (text, number, boolean, select)
- Mod source types are valid (modrinth, umod, workshop)
- Dynamic options providers exist for referenced sources

## Fake Worker

A stateful in-memory implementation of the `Worker` interface. Lives in `testutil/fake_worker.go`.

### Capabilities
- Tracks volumes as temp directories (real filesystem for file op tests)
- Tracks containers with state machine (created → running → stopped)
- Emits container events to a channel (start, die, stop) for StatusManager/ReadyWatcher
- Simulates ready pattern by writing log lines after start
- Configurable delays (e.g., simulate slow image pull)
- **Fault injection:** `FailNext(method string)` causes the next call to that method to return an error
- Multiple instances for multi-node tests (each fake = one worker node)

### Limitations
- No real container isolation or networking
- No real image pulling (images are "pulled" instantly)
- Port bindings tracked but not opened on host
- File operations use temp dirs, not Docker volumes

## Test Helpers (`testutil/`)

| File | Purpose |
|------|---------|
| `db.go` | `NewTestDB()` — in-memory SQLite with migrations applied |
| `fake_worker.go` | Fake `Worker` implementation with state tracking and fault injection |
| `fixtures.go` | Test game definition, helper functions for common setup |
| `services.go` | `NewTestServices()` — wires all services with fake workers, returns service bundle |
| `api.go` | `NewTestAPI()` — returns `httptest.Server` with full router and middleware |

### Key Helpers

```go
// Quick gameserver creation for tests that need one but aren't testing creation itself
func CreateTestGameserver(t *testing.T, svc *ServiceBundle, opts ...GameserverOption) *models.Gameserver

// Create tokens for auth tests
func MustCreateAdminToken(t *testing.T, svc *ServiceBundle) string
func MustCreateCustomToken(t *testing.T, svc *ServiceBundle, perms []string, gameserverIDs []string) string

// Register a fake worker node for multi-node tests
func RegisterFakeWorker(t *testing.T, svc *ServiceBundle, nodeID string, opts ...WorkerOption) *FakeWorker

// Wait for an event type to be published (with timeout)
func WaitForEvent(t *testing.T, bus *EventBus, eventType string, timeout time.Duration) Event
```

## File Layout

```
testutil/
    db.go
    fake_worker.go
    fixtures.go
    services.go
    api.go
testdata/
    test-game.yaml          # minimal stable game definition for most tests
models/
    gameserver_test.go
    token_test.go
    ...
service/
    gameserver_test.go
    gameserver_lifecycle_test.go
    gameserver_ports_test.go
    gameserver_migration_test.go
    auth_test.go
    permissions_test.go
    backup_test.go
    schedule_test.go
    events_test.go
    webhook_test.go
    settings_test.go
    mod_test.go
    ...
api/handlers/
    gameservers_test.go
    auth_test.go
    ...
worker/
    local_test.go           # //go:build integration
    fileops_test.go         # //go:build integration
    logparse_test.go        # no docker needed
    ...
games/
    store_test.go           # validates real game definitions
```

## TODOs

- **Test both Docker and Podman** — Integration tests currently run against whichever runtime auto-detects first. Need a way to explicitly test both (e.g., `GAMEJANITOR_TEST_SOCKET` env var with `test-docker`/`test-podman` flake scripts). The sidecar permission bug may behave differently between runtimes (Podman rootless maps UIDs differently).

## Running Tests

```bash
# All tests except Docker integration
go test ./...

# Include Docker integration tests
go test -tags integration ./...

# Specific package
go test ./service/...

# Verbose with test names
go test -v ./service/... -run TestMigration

# Race detector (important for concurrent port allocation tests)
go test -race ./service/...
```

These commands should be added to the nix flake devShell as convenience scripts:
- `test` — `go test ./...`
- `test-all` — `go test -tags integration ./...`
- `test-race` — `go test -race ./...`

## Implementation Plan

### Phase 1: Foundation — DONE

Built `testutil/` package: test DB, fake Worker, service wiring, API test server, test game definition.

### Phase 2: Validate — DONE

19 tests proving the foundation works: gameserver CRUD, placement, port allocation, auth token scoping.

### Phase 3: Broad Coverage — DONE

166 tests across all packages:
- `models/` — 7 files (~80 tests): CRUD, filters, cascades, allocation queries, JSON columns
- `service/` — 8 files (~45 tests): lifecycle, placement, ports, auth, permissions, settings, events, backups, schedules
- `api/handlers/` — 3 files (~18 tests): CRUD endpoints, auth middleware, security headers, games API
- `games/` — 1 file (~10 tests): game definition validation, regex, env types, local overrides
- `worker/` — 1 file (~10 tests): log parsing, Docker multiplexed format, auto-detection
- `naming/` — 1 file (~7 tests): naming conventions, 4 skipped bug cases

Bugs found: PortMode default, naming rejection, nil worker panics (2 fixed). See `TESTING_BUGS.md`.

### Phase 4: Multi-Node & Races — DONE

Migration (7 tests), concurrent port allocation (3 tests), resource enforcement (7 tests).

### Phase 5: Webhook Delivery & API Permissions — DONE

Webhook HMAC/retry/state (7 tests), gameserver updates (5 tests), API permission matrix (5 tests).

### Phase 6: Worker Integration — DONE

Docker container lifecycle and event watching (2 pass). Sidecar file ops and path traversal (4 skipped — documented bugs).

### Phase 7: Async Pipeline — DONE

`NewTestServicesWithSubscribers` helper. Status derivation, event persistence, status_changed events (5 tests).

### Phase 8: Service Depth — file, console, ready watcher

These services delegate to the fake worker and are straightforward to test. Currently 0% coverage.

**`service/file_test.go`** (new file):
- ListFiles, ReadFile, WriteFile, DeletePath, CreateDirectory, RenamePath — happy paths through the dispatcher
- Path validation: `/data` prefix enforcement, traversal rejection at the service layer
- Gameserver not found → error

**`service/console_test.go`** (new file):
- GetLogs — returns log reader for a running gameserver
- GetLogs — gameserver not found, not running
- SendCommand — happy path, verify exec called on worker
- SendCommand — game doesn't support commands (disabled capability)
- ListLogSessions, GetLogSession — historical log file listing

**`service/ready_test.go`** (new file, uses `NewTestServicesWithSubscribers`):
- Ready pattern detected — start gameserver, verify status promoted to running
- Install marker detected — start gameserver, verify `installed` flag set to true in DB
- No ready pattern (game has none) — immediate promotion after start
- Ready watcher stopped on gameserver stop

### Phase 9: API Handler Coverage

Currently 8%. Each handler is thin (parse → call service → format response), but input validation and error mapping happen here. Test the contract, not the business logic.

**`api/handlers/backups_test.go`** (new file):
- POST /api/gameservers/{id}/backups — 201 with backup record
- GET /api/gameservers/{id}/backups — list
- DELETE /api/gameservers/{id}/backups/{backupId} — 204
- POST restore — verify accepted response

**`api/handlers/schedules_test.go`** (new file):
- CRUD endpoints — 201, 200, 204
- Invalid cron → 400
- Invalid type → 400

**`api/handlers/files_test.go`** (new file):
- GET list, GET content, PUT content, DELETE, POST upload, POST mkdir
- Path traversal attempt → 400
- Gameserver not found → 404

**`api/handlers/settings_test.go`** (new file):
- GET /api/settings — returns all settings
- PATCH /api/settings — update a value, verify it persists
- Read-only settings can't be changed

**`api/handlers/webhooks_test.go`** (new file):
- CRUD endpoints
- POST test delivery
- GET deliveries list

### Phase 10: Mod Service (stubbed HTTP)

The mod sources hit external APIs, but `ModService` has internal logic worth testing: loader selection, install path resolution, DB tracking, file cleanup on uninstall. Stub the HTTP calls.

**`service/mod_test.go`** (new file):
- InstallMod — happy path with a fake mod source that returns a file
- UninstallMod — removes file and DB record
- ListInstalledMods — returns mods for a gameserver
- InstallMod — precondition check (requires_env not set)
- InstallMod — already installed → error
- Mod source validation — unknown source type rejected

Requires a `FakeModSource` or HTTP test server to avoid hitting Modrinth/uMod/Workshop.

### Phase 11: StatusManager & Recovery

The highest-complexity untested area. StatusManager watches container events, handles auto-restart, and reconciles DB state on startup. Requires careful setup with `NewTestServicesWithSubscribers`.

**`service/status_manager_test.go`** (new file):
- Container "die" event → gameserver status set to stopped
- Container "die" on running gameserver with auto_restart → gameserver restarted
- Auto-restart capped at 3 attempts → stops retrying, sets error state
- Auto-restart counter reset when gameserver reaches running
- Stale container event (old ContainerID) → ignored
- Recovery on startup: DB says "running" but container is gone → status corrected to stopped

### Archetype Scenario Tests — DONE

End-to-end workflows from each user archetype's perspective, testing the full stack above the Worker interface.

**Newbie/homelab:** zero-config create→start→stop, safe defaults, SFTP credentials, password regeneration
**Power user:** multi-node placement + migration, scoped tokens, backup + schedule workflow, file management with path traversal
**Business:** business mode secure defaults, resource limits enforced, webhook setup, capacity planning across a 3-node cluster
**API-level:** full HTTP workflow (newbie), auth enforcement (business), response envelope consistency

### Not planned (unit tests)

| Area | Reason |
|------|--------|
| `worker/process.go` | Process runtime with bwrap/Box64. Platform-specific, needs manual testing. |
| `worker/remote.go` | gRPC client. Tested implicitly by the fake worker abstraction. |
| `worker/agent.go` | gRPC server. Needs both sides running. |
| `worker/oci.go` | OCI image pull/extract. Network-dependent. |
| `service/mod_source_*.go` | External API clients. Simple HTTP+JSON, low bug density. |
| `service/backup_store.go` S3 path | Needs a real or mocked S3 endpoint. LocalStore tested via backup tests. |

---

## True End-to-End Tests

Everything above tests the orchestration logic with a fake worker. The fake worker returns instantly, has no real containers, no real ports, no real log streams. This is the right approach for fast, reliable unit/integration tests.

But there's a class of bugs we can only catch with real containers:
- Game ready patterns that don't match actual game output
- Docker volume permissions (the sidecar bug)
- Real port binding conflicts
- Container resource limits actually applied
- Backup/restore data integrity through real tar/gzip streams
- SFTP access to real volume data
- Container event timing (start → ready → die)

### Three-Tier Strategy

Real games are too slow and fragile for automated testing (network downloads, large images, version breakage). Instead, three tiers with escalating realism:

**Tier 1: Fake worker (current, 325+ tests, <7s)**
Tests all orchestration logic with in-memory state. No Docker needed. Run on every commit.

**Tier 2: Test game image (~15 tests, ~60s)**
A purpose-built image using the **real base image** (`images/base/`) with fake game scripts. Same entrypoint.sh, same user (1001:1001), same volume layout, same log rotation — but the "game" is a shell script that starts in <1s. Tests the real Docker/Podman integration without downloading game binaries.

**Tier 3: Real game smoke tests (~3 tests, ~5min)**
Actually starts a real game (Terraria/TShock is fastest: ~3s start, ~300MB image, GitHub download). Verifies the full chain: image pull → install → ready pattern → query response → stop. Run nightly or before releases, not on PRs.

### Tier 2: Test Game Image

Lives in `testdata/test-game-image/`. Uses `FROM ghcr.io/warsmite/gamejanitor/base` — the exact same entrypoint that production game containers use. The fake scripts in `testdata/games/test-game/scripts/` are bind-mounted at `/scripts/` by gamejanitor, just like real games.

**install-server:** Writes a marker file to `/data`, prints install output, takes <1s.
**start-server:** Starts a simple TCP listener on `$GAME_PORT`, prints the ready pattern, handles SIGTERM cleanly.
**send-command:** Echoes input to stdout (simulates RCON response).
**save-server:** No-op (simulates world save).

The image itself is just the base — no game binaries, no downloads, no network dependencies. Scripts are mounted from the host, exactly like production.

**What Tier 2 catches that Tier 1 can't:**
- Volume ownership and permissions (the sidecar 1001:1001 bug)
- Real port binding on the host (TCP dial verification)
- Entrypoint.sh log rotation and install marker emission
- Container stop timing (SIGTERM → exit)
- SFTP access to real volume data
- Backup tar/gzip through real filesystem
- The ReadyWatcher parsing real log output (not a pre-filled buffer)

**Build-tagged:** `//go:build e2e`. Run with `go test -tags e2e -timeout 5m ./e2e/`.

**Test scenarios:**

Lifecycle:
- Create → start → wait for ready pattern in real logs → verify "running" → stop → verify "stopped"
- First start: verify install marker detected, `installed=true` in API
- Second start: verify `SKIP_INSTALL=1` passed, install script not re-run
- Restart: verify container ID changes
- Delete: verify container and volume gone from Docker

File access:
- SFTP login with create response credentials → upload file → read via API
- API file write → verify via SFTP read
- Path traversal blocked in both directions

Backup/restore:
- Write files to volume → backup → wipe volume → restore → verify files intact
- Backup size in API matches actual data

Ports:
- Two gameservers of same game → different host ports → both TCP-dialable

### Tier 3: Real Game Smoke Tests

Build-tagged with `//go:build smoke`. Generic — works with any game in the game store.

```bash
# Default: Terraria (fastest to install/start)
go test -tags smoke -timeout 10m ./e2e/

# Specific game
SMOKE_GAME=minecraft-java go test -tags smoke -timeout 15m ./e2e/

# Multiple games (CI nightly)
SMOKE_GAMES=terraria,minecraft-bedrock,valheim go test -tags smoke -timeout 30m ./e2e/

# All supported games
SMOKE_GAMES=all go test -tags smoke -timeout 60m ./e2e/
```

**Design: game-agnostic test function.**

The test is a single `TestSmoke` that reads everything from the game definition — no game-specific code:

```go
func TestSmoke(t *testing.T) {
    games := smokeGames() // reads SMOKE_GAME(S) env, defaults to "terraria"
    for _, gameID := range games {
        t.Run(gameID, func(t *testing.T) {
            runSmokeTest(t, gameID)
        })
    }
}
```

`runSmokeTest` loads the game.yaml, fills env vars from defaults/autogenerate, and runs:

1. **Create** — using game defaults, auto-filling required env vars
2. **Start** — wait for `ready_pattern` from game.yaml (compiled as regex)
3. **Verify installed** — check `installed=true` in API response
4. **Query** (if game has `gjq_slug`) — verify GJQ returns a response
5. **Send command** (if game has `command` capability) — verify non-error response
6. **Stop** — verify clean exit within timeout
7. **Cleanup** — delete gameserver, verify container + volume removed

**Per-game considerations handled automatically:**
- **Required env vars:** filled from `default` or `autogenerate` in game.yaml. If a var is `required: true` with no default and no autogenerate, the test skips with a message.
- **Consent-required vars:** auto-accepted (e.g., Minecraft EULA=true).
- **Install timeout:** scales with game — env var `SMOKE_INSTALL_TIMEOUT` (default 5min).
- **Ready timeout:** env var `SMOKE_READY_TIMEOUT` (default 2min).
- **Disabled capabilities:** query and command steps skipped if the game disables them.
- **Image availability:** test skips if the base image isn't built locally.

**Default game: Terraria.**
Fastest real game (~3s start, ~30s install, ~300MB image, no Steam dependency). Good default for PR gates and local testing.

**Game compatibility matrix (approximate):**

| Game | Install | Start | Image | Notes |
|------|---------|-------|-------|-------|
| Terraria | ~30s | ~3s | base | GitHub download, no Steam |
| Minecraft Java | ~30s | ~15s | java (~1GB) | Mojang download, needs EULA |
| Minecraft Bedrock | ~60s | ~5s | base | Direct download |
| Valheim | ~3min | ~10s | steamcmd | SteamCMD, large |
| Rust | ~5min | ~30s | steamcmd | SteamCMD, very large, slow |
| Counter-Strike 2 | ~10min | ~20s | steamcmd | Huge, very slow |

### Prerequisites

- Docker or Podman running
- Base image built (`build-image base`). For SteamCMD games: `build-image steamcmd`. For Java games: `build-image java`.
- `test-e2e`, `test-smoke` scripts in flake.nix

### When to run

| Tier | Trigger | Time |
|------|---------|------|
| 1 (fake worker) | Every commit, every PR | <7s |
| 2 (test game image) | Every PR, pre-merge | ~60s |
| 3 (smoke, default game) | Pre-merge, nightly | ~2min |
| 3 (smoke, all games) | Pre-release | ~30min |

---

## Long-Term Testing Vision

The test suite should evolve alongside the three user archetypes:

**Newbie tests** — As the onboarding UX matures, tests should cover the first-run experience: game selection, one-click create, "it just works" verification. These should be the project's smoke tests — if they break, nothing works.

**Power user tests** — As the CLI is rewritten, tests should cover the CLI contract: `gamejanitor gameserver create`, context switching between controllers, `gamejanitor install` for systemd setup. These protect the API contract that power users depend on.

**Business tests** — As multi-node and the hosting service grow, tests should cover scale (100+ gameservers on 10+ nodes), webhook delivery reliability under load, API versioning/backwards compatibility, and the billing integration surface. The business mode defaults should be the hardest-tested path because paying customers have the lowest tolerance for bugs.

## What We Explicitly Don't Test

- **UI/frontend** — separate concern, needs its own frontend testing strategy (Playwright or similar)
- **Generated protobuf** — generated code, tested implicitly by gRPC usage
- **OCI image pulling** — network-dependent, tested manually
- **Box64/bwrap** — platform-specific, tested manually on target hardware
- **External mod APIs** — Modrinth, uMod, Steam Workshop (network-dependent, stub at HTTP level if needed later)
