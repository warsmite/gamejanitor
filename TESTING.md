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

### Phase 4: Multi-Node & Races

The multi-node orchestration paths — the highest-risk area.

**`service/gameserver_migration_test.go`** (new file):
- TestMigration_HappyPath — migrate worker-1 → worker-2, verify node_id updated, ports reallocated on target
- TestMigration_TargetNodeMustHaveCapacity — target node full, expect error
- TestMigration_SourceAndTargetMustBeOnline — target worker not registered, expect error
- TestMigration_FailureDuringTransfer — FailNext("RestoreVolume"), verify source data preserved
- TestMigration_AutoTrigger — update resources beyond current node, verify migration fires

**`service/gameserver_ports_test.go`** (extend):
- TestPortAllocation_ConcurrentCreates_NoDuplicatePorts — N goroutines creating simultaneously, verify all allocated ports unique
- TestPortAllocation_PortsFreedOnDelete — create, delete, create again, verify reuse
- TestPortAllocation_MultipleGameserversFillRange — create several, verify contiguous blocks don't overlap

**`service/resource_enforcement_test.go`** (new file):
- TestResourceEnforcement_MemoryExceedsNodeLimit
- TestResourceEnforcement_CPUExceedsNodeLimit
- TestResourceEnforcement_StorageExceedsNodeLimit
- TestResourceEnforcement_RequireMemoryLimitSetting — enable setting, create with 0 memory, expect error
- TestResourceEnforcement_RequireCPULimitSetting
- TestResourceEnforcement_RequireStorageLimitSetting
- TestResourceEnforcement_ZeroMemoryMeansUnlimited — known bug from MEMORY.md

### Phase 5: Webhook Delivery & API Permissions

**`service/webhook_delivery_test.go`** (new file):
- TestWebhookDelivery_HMACSignature — verify X-Webhook-Signature header is correct HMAC-SHA256
- TestWebhookDelivery_RetryBackoff — verify exponential backoff calculation
- TestWebhookDelivery_MaxAttempts — verify delivery marked failed after max attempts
- TestWebhookDelivery_EventFiltering — wildcard, namespace glob, specific event type
- TestWebhookDelivery_DisabledEndpointSkipped

**`api/handlers/permissions_test.go`** (new file):
- TestAPI_PermissionMatrix — for each protected endpoint, verify custom token without the right permission gets 403
- TestAPI_GameserverScoping — custom token scoped to gs-1 can't access gs-2 via API
- TestAPI_AdminEndpoints_RejectCustomTokens — POST /api/gameservers, /api/tokens, /api/workers

**`service/gameserver_update_test.go`** (new file):
- TestUpdate_NameChange
- TestUpdate_EnvChange
- TestUpdate_NonAdminBlockedFromResources — non-admin can't change memory/CPU
- TestUpdate_EnvTriggersReinstall — changing triggers_install env var clears installed flag

### Phase 6: Worker Integration (Docker required)

Build-tagged with `//go:build integration`. Requires Docker daemon.

**`worker/local_test.go`**:
- TestWorker_ContainerLifecycle — pull Alpine, create, start, inspect, stop, remove
- TestWorker_VolumeOperations — create, write, read, list, delete
- TestWorker_DirectAccessDetection — verify probe and caching
- TestWorker_BackupRestoreRoundTrip — backup volume, restore to new volume, verify data integrity
- TestWorker_FilePathTraversal — attempt `../../etc/passwd`, verify rejected

### Subagent Note

Subagents are useful for read-only research (codebase exploration, spec review) but cannot write files in this project's permission configuration. All test writing is done directly in the main conversation.

## What We Explicitly Don't Test

- **UI/frontend** — separate concern, not covered by Go test suite (needs its own frontend testing strategy)
- **CLI commands** — thin wrappers over API client, low bug density
- **Generated protobuf** — generated code, tested implicitly by gRPC usage
- **OCI image pulling** — network-dependent, tested manually
- **Box64/bwrap** — platform-specific, tested manually on target hardware
- **External APIs** — Modrinth, uMod, Steam Workshop (network-dependent, stub at HTTP level if needed later)
