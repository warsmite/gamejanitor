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

### Phase 1: Leaf renames [DONE]

- [x] `models/` → `model/`, `validate/` → `pkg/validate/`
- [x] `naming/` → `pkg/naming/`, `netinfo/` → `pkg/netinfo/`, `tlsutil/` → `pkg/tlsutil/`
- [x] `constants/` deleted, values inlined
- [x] `api/handlers/` → `api/handler/`

### Phase 2: Worker sub-packages [DONE]

- [x] `worker/logparse.go` → `worker/logparse/`
- Remaining worker/ restructuring deferred — LocalWorker methods can't split without major refactor

### Phase 3: Controller parent package [DONE]

- [x] `controller/eventbus.go` — EventBus, WebhookEvent, StatusEvent
- [x] `controller/errors.go` — ServiceError + error constructors
- [x] `controller/status.go` — status constants + helpers (exported)
- [x] `controller/events.go` — all event types, constants, Actor, AllEventTypes

### Phase 4: Controller domain packages [PARTIAL]

**Done:**
- [x] 4a: `controller/auth/` — AuthService, permissions, token context
- [x] 4b: `controller/settings/` — SettingsService
- [x] 4c: `controller/event/` — EventHistoryService (EventStore stays in service/ — circular dep)
- [x] 4e: `controller/orchestrator/` — Dispatcher, Registry, ControllerGRPC (from worker/)
  - WorkerNodeService stays in service/ (depends on service event types)

**Remaining — service/ still has 25 files:**

The remaining domain services (gameserver, backup, status, schedule, webhook, mod)
are tightly coupled through shared types within service/. Each references types
from multiple siblings (GameserverService, BackupStore, EventBus, Dispatcher, etc).

Extracting them requires cross-service interfaces at each boundary. Now that events,
auth, settings, and orchestrator are extracted, the remaining service/ files only
cross-reference each other — they could be split using the interface pattern described
above. This is mechanical but time-consuming work.

- [ ] 4f: `controller/gameserver/` — 7 files (gameserver, lifecycle, ports, migration, inspect, console, file)
- [ ] 4g: `controller/backup/` — 2 files (backup, backup_store)
- [ ] 4h: `controller/status/` — 5 files (status manager, query, ready, stats_poller, subscriber)
- [ ] 4i: `controller/schedule/` — 2 files (schedule, scheduler)
- [ ] 4d: `controller/webhook/` — 2 files (webhook, endpoint)
- [ ] 4j: `controller/mod/` — 5 files (mod, source, modrinth, umod, workshop)
- [ ] Move event_store.go and worker_node.go to appropriate homes

### Phase 5: Cleanup

- [ ] Delete empty `service/` directory
- [x] Delete `constants/` directory
- [x] Delete `validate/` directory (moved to pkg/)
- [ ] Delete empty `naming/`, `netinfo/`, `tlsutil/` directories (moved to pkg/)
- [ ] Run full test suite
- [ ] Run `go vet ./...`
- [ ] Verify `go build ./cli/` works
- [ ] Verify dev server starts (`nix develop` + reflex)
