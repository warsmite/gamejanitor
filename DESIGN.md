# Gamejanitor Architecture

## Overview

Gamejanitor manages game server processes across one or more machines. A single binary runs as controller, worker, or both. The controller owns all domain logic. Workers are dumb compute — they run sandboxed processes, manage volumes, and report events.

The core design principle: **each gameserver is a runtime object in the controller's memory that owns its own lifecycle.** It is not a database row operated on by services. It knows what it's doing, receives worker events, and can always answer "what's happening?" by looking at its own state.

## System Layers

### Worker

Runs processes and manages data. Knows nothing about "gameservers" — operates on containers, volumes, and files. Responsibilities span four areas:

- **Instances**: create, start, stop, kill, inspect, exec, stream logs, collect stats
- **Volumes**: create, remove, backup (tar stream out), restore (tar stream in), size
- **Files**: read, write, list, delete, rename, download within a volume
- **Depot**: download game files (Steam/other), prepare game scripts, cache management

Currently implemented as a single `Worker` interface (~34 methods). Splitting into focused interfaces is tracked as tech debt — the monolithic interface works but consumers that only need files or volumes depend on everything.

`SandboxWorker` implements the interface locally using bubblewrap, slirp4netns, and systemd scopes. `RemoteWorker` wraps the same interface over gRPC.

Workers push instance lifecycle events (started, ready, exited) via a persistent gRPC stream. The controller never polls for process state.

### Controller

Owns the lifecycle of every gameserver. Holds one runtime object per gameserver in memory. On startup, loads gameservers from the database and reconciles with workers to rebuild runtime state. From that point forward, the in-memory objects are the authority for anything operational.

### API

Translates HTTP into method calls on gameserver objects. Validates input, calls methods, returns results. Contains no business logic.

## The Gameserver Object

The central concept in the entire system.

### State

Two categories, separated by durability:

**Durable state** — persisted to DB, survives controller restarts:

- Identity: ID, name, game ID, volume name
- Configuration: ports, env, resource limits, port mode, connection address, auto-restart
- Ownership: created-by token, grants
- Placement: node ID, node tags
- User intent: desired state (`running`, `stopped`, `archived`)
- Runtime references: instance ID (currently deployed container)
- Install state: installed flag, applied config snapshot
- SFTP: username, hashed password
- Error: last error reason (persisted so crash reasons survive controller restarts)

**Runtime state** — in-memory only, rebuilt from workers on recovery:

- Current operation: type, phase, progress (nil when idle)
- Process state: last worker report (running/exited, exit code, started-at)
- Crash counter: reset on user-initiated start, incremented on unexpected death
- Worker reference: the worker connection for this gameserver's node (nil if offline)

### Lifecycle Methods

All lifecycle methods live on the gameserver object:

- `Start(ctx)` — download depot, pull image, install if needed, create and start instance
- `Stop(ctx)` — run stop script, stop instance, remove instance
- `Restart(ctx)` — stop then start
- `UpdateGame(ctx)` — stop, pull latest image, run update script, start
- `Reinstall(ctx)` — stop, wipe volume, full install and start
- `Archive(ctx)` — stop, backup volume to storage, remove volume, mark archived
- `Unarchive(ctx, targetNode)` — restore volume from storage, assign to node
- `Migrate(ctx, targetNode)` — stop, transfer volume via storage, start on target
- `UpdateConfig(ctx, changes)` — apply configuration changes, persist to DB

Each method:
1. Acquires the object's mutex
2. Checks preconditions against its own state (is there already an operation? is the worker available? is the gameserver in a valid state for this action?)
3. Rejects if preconditions fail
4. Sets `operation` to track the work
5. Spawns a goroutine for long-running work
6. Returns immediately (caller gets 202 Accepted)

The goroutine updates the operation's phase and progress as it works. On completion (success or failure), it clears the operation and sets appropriate state (error reason on failure, cleared error on success).

### Operation Guard

The object's mutex + `operation` field IS the guard. No external guard mechanism needed.

- If `operation != nil`, a new lifecycle operation is rejected (except stop and delete, which can interrupt).
- Stop sets a cancellation signal that the running operation checks between steps.
- The mutex serializes all state access per-gameserver.

If the controller crashes mid-operation, the operation is lost. On recovery, the controller inspects workers to determine actual state and corrects accordingly. No operation state is persisted because it cannot be meaningfully resumed — the goroutine and its context are gone.

The `instance_id` in the database serves as a hint for recovery: if set, the controller checks whether that instance is still running on the worker.

### Worker Event Handling

When the worker reports an instance lifecycle event, the Manager routes it to the correct gameserver object:

- **Instance running/ready**: object records process state, sets started-at, clears error, publishes ready event. Stats and query polling begin.
- **Instance exited (unexpected)**: object records error reason (with exit code interpretation — OOM, signal, etc.), increments crash counter, stops polling, publishes error event. If auto-restart is enabled and crash count is under the limit, triggers a restart.
- **Instance exited (expected)**: during a stop operation, the object is already handling cleanup. The exit event is acknowledged but doesn't trigger error handling.

Stale events (from old instances after a stop/restart cycle) are ignored by checking the event's instance ID against the object's current instance ID.

### Answering "What is this gameserver doing?"

The object looks at its own fields:

```
if desired_state == "archived" → archived
if worker == nil              → unreachable  
if operation != nil           → operation phase (installing, starting, stopping, ...)
if error_reason != ""         → error
if process is running         → running
otherwise                     → stopped
```

No derivation from external systems. No caches to consult. No synthesis across components. The object knows because it's the one in charge.

### Snapshots

For API responses, the object produces a read-only snapshot of its current state: all durable fields plus computed display fields (status, operation, connection host, SFTP port, restart-required flag, node info). This is the only way external code reads gameserver state.

## The Manager

Holds all gameserver objects in a map. Single point of entry for the gameserver domain.

### Responsibilities

- **Create**: validate configuration, select node via placement service, allocate ports, create volume on worker, write DB record, instantiate the runtime object.
- **Delete**: delegate to the object (stop if running, clean up infrastructure), remove DB record, remove from map.
- **Lookup**: by ID (returns the live object), by filter (returns snapshots). List produces snapshots from all objects, filterable by game, status, node, IDs.
- **Recovery**: on startup — load all records from DB, create objects, inspect every connected worker for running instances, populate runtime state. On worker reconnect — recover gameservers assigned to that worker.
- **Event routing**: map incoming worker instance events (by instance name) to the correct gameserver object and deliver.
- **Worker lifecycle**: when a worker comes online (via registry callback), assign it as the worker reference for gameservers on that node and trigger per-worker recovery. When a worker goes offline, clear worker references for affected gameservers.

### Placement

Node selection for new gameservers and migrations. Considers:
- Resource capacity (memory, CPU, storage limits minus allocated)
- Node tags (gameserver requires specific labels)
- Port availability (allocate from range, avoid conflicts)
- Cordon status (cordoned nodes don't accept new placements)

Port allocation uses a reservation system: ports are "pending" during creation so concurrent creates don't collide, then committed once the DB record is written.

## Cluster Layer

### Registry

Tracks worker health via heartbeats. Workers register with capabilities and resource limits. The registry fires callbacks when workers come online or go offline.

### Watcher

The Manager owns per-worker watch goroutines — one per connected worker — reading the instance event gRPC stream and routing events to the correct gameserver object.

On stream error, the Manager actively verifies the worker is still reachable via a short-timeout health check RPC:
- Reachable → stream died but worker is alive, restart the watch goroutine
- Unreachable → mark worker offline immediately (don't wait for heartbeat timeout)

This gives ~3s detection of worker death instead of the 30s heartbeat timeout, while tolerating transient stream failures.

### Stats Poller

Periodically collects resource usage (CPU, memory, network, disk) from workers for running instances. Publishes stats events on the bus for SSE and stores samples in the time-series stats table. Polling starts when a gameserver reaches running state, stops when it leaves.

### Query Service

Periodically queries game server ports using game-specific protocols (Source Engine A2S, Minecraft ping, etc.) for player count, map, version. Publishes query data events for SSE. Same start/stop lifecycle as stats polling.

## Supporting Services

Independent domains that interact with the gameserver Manager when they need lifecycle coordination.

### Backup

- Create: stop gameserver (via its object), stream volume tar from worker, gzip, save to storage (local filesystem or S3), restart if was running. Concurrency-limited (3 simultaneous) to avoid CPU/memory saturation from compression.
- Restore: stop gameserver, restore volume from storage tar stream, restart.
- Storage abstraction: local filesystem and S3 backends behind a common interface.
- Gameserver archive/unarchive uses the same storage backend but is orchestrated by the gameserver object itself, not the backup service.

### Mods

- Multi-source catalog: search and metadata from Modrinth (Minecraft), uMod (Rust/Carbon), Steam Workshop. Each source is a stateless query engine behind a common catalog interface.
- Install: download mod file, place on volume via worker file operations, record in DB.
- Modpack support: install a pack (set of mods) atomically, track which mods belong to which pack.
- Reconciliation: before a gameserver starts, verify that all DB-tracked mods exist on the volume. Re-download missing files. Called by the gameserver object during its start sequence.

### Files

Pure passthrough to the worker's file interface. Validates paths (prevent traversal), routes to the correct worker via dispatcher. No gameserver lifecycle interaction.

### Console

- Stream logs from a running instance via the worker's log streaming.
- Send commands to a running instance via exec.
- Historical log access from persisted log files on the volume.

### Schedule

Cron-based automation using a cron library (not hand-rolled). Supported actions: restart, update game, backup, send command. On trigger, calls the appropriate method on the gameserver object or supporting service.

Catch-up logic: if the controller was down and missed a schedule, run it on startup (within a configurable window).

### Auth

Token-based authentication. Three roles: admin (full access), user (scoped to owned gameservers + granted access), worker (gRPC registration only). Per-gameserver grants allow fine-grained permission control (start/stop, configure, manage files, etc.). Quotas on user tokens (max gameservers, max memory, max CPU, max storage).

### Settings

Global runtime configuration stored in the database. Type-safe accessors with caching. Profile-based defaults (standalone vs business mode). Applied on startup from config file, queryable and mutable via API.

### Webhooks

Subscribe to the event bus, deliver payloads to external HTTP endpoints with exponential backoff retry. Endpoint CRUD with event type filtering.

### Warnings

Subscribe to stats events on the bus. Detect threshold violations (storage usage approaching limit). Emit warning events. Deduplicate so each condition fires once until resolved.

## Database

SQLite in WAL mode. Single file. Stores durable facts only.

### What is stored

- Gameserver identity, configuration, desired state, node assignment, install state, instance ID, error reason
- Backup records (metadata + storage reference)
- Installed mod records
- Schedule definitions
- Auth tokens and grants
- Settings key-value pairs
- Worker node registration data
- Event history (audit log)
- Webhook endpoints and delivery queue
- Time-series stats samples (with downsampling tiers)

### What is NOT stored

- Gameserver "status" — there is no status column. Status is a runtime concept answered by the gameserver object.
- Operation state — transient, lives only in the goroutine and object fields.
- Worker process reports — delivered to objects in real-time, not persisted. `error_reason` is the one exception (persisted so crash reasons survive controller restarts).
- Worker connection state — tracked by the registry in memory.

### Schema principles

- No migration files during pre-release — single schema file, modified directly.
- JSON columns for structured config (ports, env, tags, grants) — acceptable for data that's always read/written as a unit and not queried by individual fields.
- Foreign keys with CASCADE deletes where parent deletion implies child cleanup.
- Indexes on hot query patterns (gameserver_id + created_at for events, resolution + timestamp for stats).

## Event System

The event bus is a simple in-process fan-out. When something happens, the responsible component publishes an event. Subscribers receive it asynchronously.

### Subscribers

- **SSE stream**: pushes events to the browser UI for real-time updates.
- **Webhook delivery**: queues events for external HTTP delivery.
- **Event persister**: writes events to the history table for the audit log API.

### What the bus is NOT

The bus is not used for internal state coordination. If one component needs to update another component's state, it calls a method. Events are announcements for external consumers, not commands between internal systems.

### Event categories

- **Lifecycle**: gameserver.started, gameserver.stopped, gameserver.ready, gameserver.error, gameserver.status_changed
- **Operations**: gameserver.operation (phase changes with progress data)
- **Infrastructure**: depot.downloading, depot.complete, image.pulling, image.pulled, instance.creating
- **Domain**: backup.completed, backup.failed, mod.installed, mod.uninstalled, schedule.task.completed
- **Cluster**: worker.connected, worker.disconnected
- **Monitoring**: gameserver.stats, gameserver.query, gameserver.warning, gameserver.reachable

## API Layer

Chi router with middleware chain: recovery → request ID → security headers → rate limiting → auth → JSON content type.

### Conventions

- No envelope — data returned directly, errors as `{"error": "message"}`.
- Async operations return 202 with the resource snapshot.
- Creates return 201. Sub-resource deletes return 204.
- All responses use typed structs.
- Permission checks via middleware, per-endpoint.

### Handler structure

One handler struct per domain (gameservers, backups, mods, files, schedules, auth, settings, webhooks, cluster, events, games, logs). Handlers hold service references, delegate all logic. No business logic in handlers.

For gameserver actions, the handler calls the gameserver object's method directly (via Manager lookup). The method returns an error if preconditions fail (409 Conflict for "operation in progress"), or nil if the operation was accepted (202 Accepted).

## Key Flows

### Start

```
1. POST /api/gameservers/{id}/actions/start
2. Handler → manager.Get(id) → gs.Start(ctx)
3. gs.Start: lock, check operation==nil, set operation, update desired_state in DB, unlock
4. Return 202 with snapshot (status shows "installing")
5. Goroutine: ensure depot → pull image → install phase → create instance → start instance
6. Each step: update operation.phase, publish progress event → SSE
7. Worker reports instance ready → manager routes → gs.HandleProcessEvent
8. gs: set process=running, clear operation, publish ready event
9. Snapshot now shows status=running
```

### Crash

```
1. Worker detects process exit → sends InstanceStateUpdate via gRPC stream
2. Watcher receives → manager.RouteEvent(instanceName, event)
3. Manager maps instance name → gameserver ID → gs.HandleProcessEvent(event)
4. gs: lock, check instance ID matches (ignore stale events)
5. Previous state was running → unexpected death
6. Set error_reason (with exit code diagnosis), persist to DB
7. Increment crash counter, stop polling, publish error + status_changed events
8. If auto_restart && crash_count <= 3: call gs.Start(ctx) internally
9. If crash limit exceeded: set error_reason to "crashed N times, auto-restart disabled"
```

### Worker Disconnect

```
1. Stream error detected → Manager actively verifies via health-check RPC
2a. Reachable → restart watch goroutine (just a stream blip)
2b. Unreachable → Registry.SetOffline, fires onWorkerOffline callback
3. Manager: iterate gameservers on that node → clear worker reference + process state
4. Stop stats/query polling for affected gameservers
5. Status becomes "unreachable" (worker==nil in the Status() check)
6. Publish worker.disconnected event

Gameservers are NOT migrated — their data lives on the dead worker's disk.
When the worker reconnects, gameservers are re-associated and recovery runs.
```

### Worker Reconnect

```
1. Worker sends heartbeat → Registry fires onWorkerRegistered callback
2. Manager: set worker reference on affected gameservers
3. Watcher: start event stream for this worker
4. Recovery: for each gameserver on this node with instance_id set:
   a. Inspect instance on worker
   b. Running → set process state, start polling
   c. Gone → clear instance_id, set error if desired_state was running
5. Status updates propagate via events
```

### Controller Restart

```
1. Load all gameserver records from DB → create runtime objects
2. For each connected worker: inspect all instances
3. Match instances to gameservers by instance name → gameserver ID mapping
4. Running instance found → populate process state, start polling
5. Instance gone but instance_id in DB → clear instance_id, set error
6. No instance, desired_state=stopped → clean state, nothing to do
7. Orphan detection: instances on workers with no matching DB record → log warning
```

### Stop Interrupting Start

```
1. gs.Start is running a goroutine (downloading depot)
2. User calls gs.Stop(ctx)
3. gs.Stop: lock, set desired_state=stopped in DB, signal cancellation, unlock
4. The start goroutine checks for cancellation between steps
5. Goroutine sees cancellation → cleans up (remove partial instance), clears operation
6. gs.Stop proceeds: stop instance if one was created, clear instance_id
```

## Dependency Graph

Construction order — no circular dependencies, no post-construction setters:

```
1. Store, EventBus, GameStore
2. SettingsService(Store)
3. BackupStorage(config)
4. Registry()
5. Dispatcher(Registry, Store)
6. Placement(Store, Dispatcher, SettingsService)
7. Manager(Store, Dispatcher, Registry, Placement,
          GameStore, SettingsService, EventBus, BackupStorage)
8. Watcher(Manager, Registry)
9. BackupService(Store, Dispatcher, Manager, BackupStorage, SettingsService, EventBus)
10. ModService(Store, Dispatcher, GameStore, EventBus)
11. FileService(Store, Dispatcher)
12. ConsoleService(Store, Dispatcher, GameStore)
13. ScheduleService(Store, Manager, BackupService, ConsoleService, EventBus)
14. AuthService(Store)
15. WebhookService(Store, EventBus)
16. WarningSubscriber(EventBus, SettingsService)
17. EventPersister(Store, EventBus)
18. EventHistoryService(Store)
```

## Single Binary, Three Modes

- **No flags** (standalone): controller + local worker. Manager creates a local SandboxWorker registered in the registry.
- **`--controller --worker=false`**: controller only. Workers connect via gRPC and register in the registry. Manager assigns gameservers to remote workers.
- **`--worker --controller=false`**: worker agent only. Runs the gRPC server, connects to the controller, executes commands. No database, no web UI, no domain logic.

## Game Definitions

Separate Go module (`games/`). Each game is a directory containing `game.yaml` (metadata, ports, env defaults, capabilities) and shell scripts (`install-server`, `start-server`, `stop-server`, `update-server`). Loaded from two sources: embedded in the binary (curated registry) and user-provided on disk. The game store resolves aliases and provides lookup by ID.

Game definitions are the contract between the controller and the scripts that run inside containers. They define what ports a game needs, what environment variables configure it, how to install and start it, and what capabilities it supports (console, query, mods).
