# Refactor Tracking

---

## Active: Architecture Refactor — Nested Package Structure

Replace the flat package layout with a nested, domain-driven structure.
Main drivers: `service/` god package (33 files, ~8k lines, 12 service types),
misplaced orchestration code in `worker/`, utility package sprawl at root.

### Target Structure

```
gamejanitor/
├── main.go
├── controller/                        # Shared: EventBus, errors, status constants
│   ├── eventbus.go                    #   (from service/broadcast.go + service/events.go)
│   ├── errors.go                      #   (from service/errors.go)
│   ├── status.go                      #   (from service/common.go)
│   ├── gameserver/                    #   GameserverService + lifecycle + ports + migration + inspect + console + file
│   ├── backup/                        #   BackupService + BackupStore interface + LocalStore + S3Store
│   ├── auth/                          #   AuthService + permissions + token context
│   ├── schedule/                      #   ScheduleService + Scheduler
│   ├── webhook/                       #   WebhookWorker + WebhookEndpointService
│   ├── event/                         #   EventStore + EventHistoryService
│   ├── mod/                           #   ModService + ModSource + modrinth/umod/workshop
│   ├── settings/                      #   SettingsService
│   ├── status/                        #   StatusManager, QueryService, ReadyWatcher, StatsPoller, StatusSubscriber
│   └── orchestrator/                  #   Dispatcher + Registry + WorkerNodeService (moved from worker/)
├── worker/                            #   Worker interface + local/remote impls
│   ├── runtime/                       #   docker (absorbs docker/), process, oci, box64
│   ├── agent/                         #   gRPC server: agent.go, grpc.go, auth.go
│   ├── logparse/                      #   Log parsing
│   ├── fileops/                       #   direct.go, sidecar.go
│   ├── backup/                        #   Worker-side backup ops
│   └── pb/                            #   Generated protobuf
├── model/                             #   Rename models/ → model/. Absorbs validate/.
├── api/                               #   HTTP transport
│   └── handler/                       #   Rename handlers/ → handler/
├── cli/                               #   CLI commands
├── pkg/                               #   Shared utilities: naming/, netinfo/, tlsutil/
├── config/                            #   Stays
├── db/                                #   Stays
├── games/                             #   Stays
├── sftp/                              #   Stays
├── ui/                                #   Stays
├── testutil/                          #   Stays
└── e2e/                               #   Stays
```

### Deleted Packages

- `constants/` (24 lines) — inline where used
- `validate/` — absorbed into `model/`
- `docker/` — absorbed into `worker/runtime/`
- `naming/`, `netinfo/`, `tlsutil/` — moved under `pkg/`

### Cross-Service Interface Pattern

Each controller sub-package defines narrow interfaces for sibling deps.
No concrete cross-imports between siblings. Wiring in `cli/serve.go`.

**Exception: `auth/` and `settings/` are true leaves** — they have zero
dependencies on other controller sub-packages. Siblings can import them
concretely (no interface needed). This is safe because the dependency
is one-directional: many packages → auth/settings, never the reverse.

```go
// controller/backup/backup.go — interface for non-leaf sibling dep
type GameserverLookup interface {
    GetGameserver(id string) (*model.Gameserver, error)
}

// controller/schedule/scheduler.go — interfaces for non-leaf sibling deps
type GameserverLifecycle interface {
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    Restart(ctx context.Context, id string) error
}
type BackupCreator interface {
    CreateBackup(ctx context.Context, gsID string) (*model.Backup, error)
}

// controller/gameserver/gameserver.go — concrete import of leaf
import "github.com/warsmite/gamejanitor/controller/settings"
```

Eliminates current `Set*()` cycle-breaking hacks.

---

### Phase 1: Leaf renames (no internal dependency changes)

- [ ] `models/` → `model/` (absorb `validate/validate.go` → `model/validate.go`)
- [ ] `naming/` → `pkg/naming/`
- [ ] `netinfo/` → `pkg/netinfo/`
- [ ] `tlsutil/` → `pkg/tlsutil/`
- [ ] Delete `constants/` — inline values into consumers
- [ ] `api/handlers/` → `api/handler/`
- [ ] Update all import paths project-wide
- [ ] Run tests, `go vet ./...`

### Phase 2: Worker sub-packages

Scoped down — most worker files are methods on LocalWorker or use unexported types
(volumeResolver), so they can't move to sub-packages without major refactoring.
`controller_grpc.go` moves to orchestrator/ in Phase 4d. docker/ stays until
the circular dep (worker↔docker) is resolved by a future LocalWorker refactor.

- [x] `worker/logparse.go` → `worker/logparse/` (standalone functions, clean separation)
- [ ] Remaining worker/ restructuring deferred — tight coupling to LocalWorker internals

### Phase 3: Controller parent package

- [ ] Create `controller/eventbus.go` — EventBus + event types (from `service/broadcast.go` + `service/events.go`)
- [ ] Create `controller/errors.go` — ServiceError (from `service/errors.go`)
- [ ] Create `controller/status.go` — status constants + helpers (from `service/common.go`)
- [ ] Update all `service.EventBus` / `service.ServiceError` / `service.Status*` refs
  - Includes remaining `service/` files + external consumers (api/, cli/, testutil/)
- [ ] Run tests

### Phase 4: Controller domain packages

One at a time, in dependency order. For each step:
1. Move files, change `package` declaration
2. Extract narrow interfaces for cross-service deps (or import leaves concretely)
3. Update remaining `service/` files that referenced moved types
4. Update external consumers (api/, cli/, testutil/, sftp/)
5. Run tests

Leaves first, then core services, then consumers of multiple siblings.

**4a — `controller/auth/`** (leaf — no deps on other controller sub-packages)
- [ ] `service/auth.go` → `controller/auth/auth.go`
- [ ] `service/permissions.go` → `controller/auth/permissions.go`
- [ ] Update remaining service/ files: `gameserver.go`, `events.go` (use `auth.TokenFromContext` etc.)
- [ ] Update api/middleware.go, api/handler/, cli/

**4b — `controller/settings/`** (leaf — no deps on other controller sub-packages)
- [ ] `service/settings.go` → `controller/settings/settings.go`
- [ ] Update remaining service/ files: `gameserver*.go`, `backup.go`, `mod*.go`
- [ ] Update api/handler/, cli/

**4c — `controller/event/`** (leaf — depends only on parent EventBus)
- [ ] `service/event_store.go` → `controller/event/store.go`
- [ ] `service/event_history.go` → `controller/event/history.go`
- [ ] Update api/router.go, api/handler/

**4d — `controller/webhook/`** (leaf — depends only on parent EventBus)
- [ ] `service/webhook.go` → `controller/webhook/webhook.go`
- [ ] `service/webhook_endpoint.go` → `controller/webhook/endpoint.go`
- [ ] Update api/router.go, api/handler/, cli/

**4e — `controller/orchestrator/`** (move from worker/ + service/)
- [ ] `worker/dispatcher.go` → `controller/orchestrator/dispatcher.go`
- [ ] `worker/registry.go` → `controller/orchestrator/registry.go`
- [ ] `service/worker_node.go` → `controller/orchestrator/worker_node.go`
- [ ] Imports `worker.Worker` interface (no cycle)
- [ ] Update remaining service/ files: all refs to `worker.Dispatcher` / `worker.Registry`
- [ ] Update api/router.go, api/handler/, cli/

**4f — `controller/gameserver/`** (deps: settings concrete, orchestrator concrete)
- [ ] `service/gameserver.go` → `controller/gameserver/gameserver.go`
- [ ] `service/gameserver_lifecycle.go` → `controller/gameserver/lifecycle.go`
- [ ] `service/gameserver_ports.go` → `controller/gameserver/ports.go`
- [ ] `service/gameserver_migration.go` → `controller/gameserver/migration.go`
- [ ] `service/gameserver_inspect.go` → `controller/gameserver/inspect.go`
- [ ] `service/console.go` → `controller/gameserver/console.go`
- [ ] `service/file.go` → `controller/gameserver/file.go`
- [ ] Define interface for BackupStore dep (from backup — not yet moved, use interface)
- [ ] Update remaining service/ files: `backup.go`, `scheduler.go`
- [ ] Update api/router.go, api/handler/, cli/

**4g — `controller/backup/`** (deps: gameserver via interface, settings concrete)
- [ ] `service/backup.go` → `controller/backup/backup.go`
- [ ] `service/backup_store.go` → `controller/backup/store.go`
- [ ] Define `GameserverLookup` interface (satisfied by gameserver.GameserverService)
- [ ] Update remaining service/ files: `scheduler.go`
- [ ] Update api/router.go, api/handler/, cli/

**4h — `controller/status/`** (deps: orchestrator concrete, gameserver via func value)
- [ ] `service/status.go` → `controller/status/manager.go`
- [ ] `service/query.go` → `controller/status/query.go`
- [ ] `service/ready.go` → `controller/status/ready.go`
- [ ] `service/stats_poller.go` → `controller/status/poller.go`
- [ ] `service/status_subscriber.go` → `controller/status/subscriber.go`
- [ ] `restartFunc` is already a `func` value — no interface needed for gameserver dep
- [ ] Update api/router.go, api/handler/, cli/

**4i — `controller/schedule/`** (deps: gameserver + backup + console via interfaces)
- [ ] `service/schedule.go` → `controller/schedule/schedule.go`
- [ ] `service/scheduler.go` → `controller/schedule/scheduler.go`
- [ ] Define `GameserverLifecycle`, `BackupCreator`, `CommandSender` interfaces
- [ ] Update api/router.go, api/handler/, cli/

**4j — `controller/mod/`** (deps: gameserver/file via interface, settings concrete)
- [ ] `service/mod.go` → `controller/mod/mod.go`
- [ ] `service/mod_source.go` → `controller/mod/source.go`
- [ ] `service/mod_source_modrinth.go` → `controller/mod/modrinth.go`
- [ ] `service/mod_source_umod.go` → `controller/mod/umod.go`
- [ ] `service/mod_source_workshop.go` → `controller/mod/workshop.go`
- [ ] Define `FileOperator` interface (satisfied by gameserver.FileService)
- [ ] Update api/router.go, api/handler/, cli/

### Phase 5: Cleanup

- [ ] Delete empty `service/` directory
- [ ] Delete empty `constants/` directory
- [ ] Delete empty `validate/` directory
- [ ] Delete empty `docker/` directory
- [ ] Delete empty `naming/`, `netinfo/`, `tlsutil/` directories
- [ ] Run full test suite
- [ ] Run `go vet ./...`
- [ ] Verify `go build ./cli/` works
- [ ] Verify dev server starts (`nix develop` + reflex)
