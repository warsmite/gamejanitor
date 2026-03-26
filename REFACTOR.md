# Refactor Tracking

---

## Active: Architecture Refactor — Store Layer + Domain Packages

### Problem

Every service holds `*sql.DB` and calls `model.GetGameserver()`, `model.ListGameservers()` etc.
directly — even for data owned by other domains. ~120 direct model DB calls across service/,
~15 of which are cross-domain (backup calling model.GetGameserver, status calling
model.UpdateGameserver, etc). This means:

1. Domain boundaries are unenforceable — sub-packages can still reach into any domain's data
2. Cross-cutting concerns (caching, audit logging, auth checks) can't be added at a single chokepoint
3. Services bypass each other, making the service layer optional rather than authoritative

### Solution: Three-layer architecture

```
api/handler/    → controller/*     → store interfaces → model/
(HTTP transport)  (business logic)   (data access)      (pure types)
```

Strict dependency direction. No layer reaches down more than one level.

### Target Structure

```
gamejanitor/
├── main.go
│
├── model/                             # Pure types + validation. ZERO SQL.
│   ├── gameserver.go                  #   Gameserver struct, GameserverFilter, validation
│   ├── backup.go                      #   Backup struct, BackupFilter
│   ├── event.go                       #   Event struct, EventFilter
│   ├── schedule.go                    #   Schedule struct
│   ├── token.go                       #   Token struct
│   ├── webhook_endpoint.go            #   WebhookEndpoint struct
│   ├── webhook_delivery.go            #   WebhookDelivery struct
│   ├── worker_node.go                 #   WorkerNode struct
│   ├── mod.go                         #   InstalledMod struct
│   ├── setting.go                     #   Setting struct
│   ├── labels.go                      #   Labels type
│   ├── pagination.go                  #   Pagination struct
│   └── validate.go                    #   Validation methods on model types
│
├── store/                             # ALL database operations. Returns model types.
│   ├── gameserver.go                  #   GameserverStore — List, Get, Create, Update, Delete, PopulateNodes
│   ├── backup.go                      #   BackupStore — List, Get, Create, Update, Delete, TotalSize
│   ├── event.go                       #   EventStore — List, Create
│   ├── schedule.go                    #   ScheduleStore — List, Get, Create, Update, Delete
│   ├── token.go                       #   TokenStore — List, Get, Create, Delete, GetByPrefix
│   ├── webhook.go                     #   WebhookStore — endpoints + deliveries CRUD
│   ├── worker_node.go                 #   WorkerNodeStore — List, Get, Create, Update, Delete
│   ├── mod.go                         #   ModStore — CRUD for installed mods
│   └── setting.go                     #   SettingStore — Get, Set, List
│
├── controller/                        # Shared: EventBus, errors, status constants, event types
│   ├── eventbus.go
│   ├── errors.go
│   ├── status.go
│   ├── events.go
│   │
│   ├── gameserver/                    # GameserverService — CRUD, lifecycle, ports, migration, console, file
│   │   ├── gameserver.go              #   Service struct + Store interface + sibling interfaces
│   │   ├── lifecycle.go               #   Start, Stop, Restart, UpdateServerGame, Reinstall
│   │   ├── ports.go                   #   Port allocation, used ports, worker limit checks
│   │   ├── migration.go              #   Cross-node migration
│   │   ├── inspect.go                #   Container info, stats, logs
│   │   ├── console.go                #   Console/command service (merged — same domain)
│   │   └── file.go                   #   File operations (merged — same domain)
│   │
│   ├── backup/                        # BackupService + file storage (local/S3)
│   │   ├── backup.go                  #   Service struct + Store interface + GameserverLifecycle interface
│   │   └── storage.go                #   BackupStorage interface + LocalStorage + S3Storage impls
│   │
│   ├── schedule/                      # ScheduleService + Scheduler (cron)
│   │   ├── schedule.go               #   CRUD service + Store interface
│   │   └── scheduler.go              #   Cron runner + sibling interfaces (GameserverOps, BackupOps, ConsoleOps)
│   │
│   ├── status/                        # Runtime monitoring — status manager, query, ready, stats
│   │   ├── manager.go                #   StatusManager — container event → status transitions
│   │   ├── query.go                  #   QueryService — A2S/GJQ polling
│   │   ├── ready.go                  #   ReadyWatcher — log pattern matching → Running promotion
│   │   ├── poller.go                 #   StatsPoller — CPU/memory/disk polling
│   │   └── subscriber.go            #   StatusSubscriber — event → status derivation
│   │
│   ├── webhook/                       # Webhook delivery + endpoint management
│   │   ├── webhook.go                #   WebhookWorker — event → HTTP delivery
│   │   └── endpoint.go              #   WebhookEndpointService — CRUD + test delivery
│   │
│   ├── mod/                           # Mod management + sources
│   │   ├── mod.go                    #   ModService + FileOperator interface
│   │   ├── source.go                 #   ModSource interface + types
│   │   ├── modrinth.go
│   │   ├── umod.go
│   │   └── workshop.go
│   │
│   ├── auth/                          # [DONE] AuthService + permissions + token context
│   ├── settings/                      # [DONE] SettingsService
│   ├── event/                         # EventStore subscriber + EventHistoryService
│   │   ├── subscriber.go            #   EventStoreSubscriber — persist events to DB
│   │   └── history.go               #   [DONE] EventHistoryService — query persisted events
│   │
│   └── orchestrator/                  # [DONE] Dispatcher + Registry + ControllerGRPC + WorkerNodeService
│       ├── dispatcher.go
│       ├── registry.go
│       ├── grpc.go
│       └── worker_node.go            #   WorkerNodeService (moves here after events moved to controller/)
│
├── worker/                            # Worker interface + implementations
│   ├── worker.go                      #   Worker interface
│   ├── types.go                       #   ContainerOptions, ContainerInfo, etc.
│   ├── local.go                       #   LocalWorker (Docker-based)
│   ├── remote.go                      #   RemoteWorker (gRPC client)
│   ├── logparse/                      #   [DONE] Log parsing
│   └── ...                            #   remaining worker files stay (tightly coupled to LocalWorker)
│
├── api/                               # HTTP transport layer
│   ├── router.go
│   ├── middleware.go
│   ├── ratelimit.go
│   └── handler/
│
├── cli/                               # CLI commands + server wiring (serve.go)
├── pkg/                               # [DONE] Shared utilities: naming/, netinfo/, tlsutil/, validate/
├── config/
├── db/
├── games/
├── sftp/
├── ui/
├── testutil/
└── e2e/
```

### Key Design Rules

**1. model/ has ZERO database queries.**
All `model.ListGameservers(db, filter)` style functions move to `store/`.
model/ is pure types, validation methods, and constants.

**2. Services receive store interfaces, not `*sql.DB`.**
Each controller sub-package defines a `Store` interface with only the queries it needs.
`store/` provides the concrete implementations. Services never see `*sql.DB`.

```go
// controller/gameserver/gameserver.go
type Store interface {
    List(filter model.GameserverFilter) ([]model.Gameserver, error)
    Get(id string) (*model.Gameserver, error)
    Create(gs *model.Gameserver) error
    Update(gs *model.Gameserver) error
    Delete(id string) error
    PopulateNodes(gs []model.Gameserver)
}

type Service struct {
    store      Store
    dispatcher Dispatcher      // interface to orchestrator
    bus        *controller.EventBus
    settings   SettingsReader  // interface to settings
    games      *games.GameStore
    log        *slog.Logger
}
```

**3. Cross-domain data access goes through sibling interfaces, never direct DB.**
Backup needs a gameserver? It calls `GameserverLookup.GetGameserver()`, not `model.GetGameserver(db)`.

```go
// controller/backup/backup.go
type GameserverLifecycle interface {
    GetGameserver(id string) (*model.Gameserver, error)
    Stop(ctx context.Context, id string) error
    Start(ctx context.Context, id string) error
}

type Service struct {
    store    Store
    gs       GameserverLifecycle  // NOT *sql.DB
    storage  Storage              // file storage (local/S3)
    bus      *controller.EventBus
}
```

**4. Leaf packages (auth, settings) can be imported concretely.**
They have zero deps on other controller sub-packages. No cycle risk.

**5. Event types stay in controller/ parent.**
All sub-packages import `controller` for event types and EventBus. No cycles.

---

## Completed Work

### Phase 1: Leaf renames [DONE]
- [x] `models/` → `model/`, `validate/` → `pkg/validate/`
- [x] `naming/` → `pkg/naming/`, `netinfo/` → `pkg/netinfo/`, `tlsutil/` → `pkg/tlsutil/`
- [x] `constants/` deleted, values inlined
- [x] `api/handlers/` → `api/handler/`

### Phase 2: Worker sub-packages [DONE]
- [x] `worker/logparse.go` → `worker/logparse/`

### Phase 3: Controller parent package [DONE]
- [x] `controller/eventbus.go` — EventBus, WebhookEvent, StatusEvent
- [x] `controller/errors.go` — ServiceError + error constructors
- [x] `controller/status.go` — status constants + helpers
- [x] `controller/events.go` — event types, constants, Actor, AllEventTypes

### Phase 4: Controller domain packages [PARTIAL]
- [x] `controller/auth/` — AuthService, permissions, token context
- [x] `controller/settings/` — SettingsService
- [x] `controller/event/history.go` — EventHistoryService
- [x] `controller/orchestrator/` — Dispatcher, Registry, ControllerGRPC

---

## Remaining Work

### Phase 5: Create store layer

Extract ~120 database query functions from `model/` into `store/`.
Each store struct holds `*sql.DB` and exposes domain-specific query methods.
`model/` becomes pure types + validation.

**5a — Create store/ package with all DB operations**
- [ ] `store/gameserver.go` — move gameserver queries from model/gameserver.go
- [ ] `store/backup.go` — move backup queries from model/backup.go
- [ ] `store/event.go` — move event queries from model/event.go
- [ ] `store/schedule.go` — move schedule queries from model/schedule.go
- [ ] `store/token.go` — move token queries from model/token.go
- [ ] `store/webhook.go` — move webhook endpoint + delivery queries
- [ ] `store/worker_node.go` — move worker node queries from model/worker_node.go
- [ ] `store/mod.go` — move installed mod queries from model/mod.go
- [ ] `store/setting.go` — move setting queries from model/setting.go
- [ ] Strip model/ down to pure types + validation
- [ ] Update all callers: `model.GetGameserver(db, id)` → `store.Get(id)` (via interface)
- [ ] Run tests

### Phase 6: Extract remaining controller sub-packages with store interfaces

Now that services receive store interfaces instead of `*sql.DB`, extraction
is clean — each sub-package defines its own Store interface + sibling interfaces.

**6a — `controller/gameserver/`**
- [ ] Move 7 files from service/ (gameserver, lifecycle, ports, migration, inspect, console, file)
- [ ] Define Store interface (gameserver queries only)
- [ ] Define sibling interfaces: ReadyWatcher, BackupDeleter
- [ ] Replace `*sql.DB` with Store in Service struct
- [ ] Replace cross-domain `model.X(db)` calls with sibling interface calls
- [ ] Eliminate `SetReadyWatcher()` hack — use constructor injection with interface
- [ ] Update api/, cli/, testutil/

**6b — `controller/backup/`**
- [ ] Move backup.go + backup_store.go from service/
- [ ] Define Store interface (backup queries only)
- [ ] Define GameserverLifecycle interface (GetGameserver, Stop, Start)
- [ ] Replace `*sql.DB` + `*GameserverService` with Store + interface
- [ ] Update consumers

**6c — `controller/status/`**
- [ ] Move 5 files (status, query, ready, stats_poller, subscriber)
- [ ] Define Store interface (gameserver queries for status checks)
- [ ] ReadyWatcher receives QueryService + StatsPoller via constructor (same package — no interface needed)
- [ ] Eliminate `SetQueryService()` / `SetStatsPoller()` hacks
- [ ] StatusManager receives `restartFunc` (already a func value — clean)
- [ ] Update consumers

**6d — `controller/schedule/`**
- [ ] Move schedule.go + scheduler.go
- [ ] Define Store interface (schedule queries)
- [ ] Define sibling interfaces: GameserverOps, BackupOps, ConsoleOps
- [ ] Replace 3 concrete service deps with interfaces
- [ ] Update consumers

**6e — `controller/webhook/`**
- [ ] Move webhook.go + webhook_endpoint.go
- [ ] Define Store interface (webhook endpoint + delivery queries)
- [ ] All event types already in controller/ — no cycle
- [ ] Update consumers

**6f — `controller/mod/`**
- [ ] Move 5 files (mod, source, modrinth, umod, workshop)
- [ ] Define Store interface (installed mod queries)
- [ ] Define FileOperator interface (satisfied by gameserver.FileService)
- [ ] Update consumers

**6g — `controller/event/subscriber.go`**
- [ ] Move event_store.go → controller/event/subscriber.go
- [ ] Define Store interface (event persistence queries)
- [ ] Rewrite `extractGameserverID()` → use `event.EventGameserverID()` (interface method)
- [ ] Rewrite `extractActor()` → add `EventActor()` method to WebhookEvent interface
- [ ] Update consumers

**6h — `controller/orchestrator/worker_node.go`**
- [ ] Move worker_node.go from service/ to controller/orchestrator/
- [ ] Define Store interface (worker node queries)
- [ ] Event types now in controller/ — no cycle
- [ ] Update consumers

### Phase 7: Cleanup + verification

- [ ] Delete empty `service/` directory
- [ ] Run full test suite
- [ ] Run `go vet ./...`
- [ ] Verify `go build ./cli/` works
- [ ] Verify dev server starts
- [ ] Review: no service holds `*sql.DB` directly
- [ ] Review: no cross-domain `model.X(db)` calls remain in controller/
- [ ] Review: all `Set*()` hacks eliminated
