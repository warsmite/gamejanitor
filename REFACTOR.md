# Refactor Tracking

Organized by refactor task, not by file.

---

## In Progress

### Architecture Findings

- **F4** `[TODO]` — **Resource cap enforcement** [MEDIUM]: Caps (MaxMemoryMB, MaxCPU, MaxBackups, MaxStorageMB) not validated on create before game defaults applied; MaxBackups/MaxStorageMB never enforced for scoped tokens on update; no bounds checking on cap values (negative/zero). Files: `gameserver.go:80,100,295-320`, `backup.go:301`

- **F5** `[DONE]` — **API inconsistencies** [MEDIUM]: Inconsistent HTTP status codes (Delete returns 204 vs 200), response envelope varies (Backup Restore returns `{status:"restored"}`), Create returns full object in some handlers but not others. Files: `response.go`, `gameservers.go`, `backups.go`, `schedules.go`, `settings_api.go`

- **F7** `[DONE]` — **Error messages leak internals** [HIGH]: Untyped `fmt.Errorf` errors propagate directly to API responses, exposing Docker errors, port range config, storage backend details, and JSON parse errors. Non-ServiceError types map to 500 with raw message. Files: `gameserver.go`, `gameserver_ports.go:143`, `backup.go:100,124`

- **F8** `[DONE]` — **Multi-node races** [MEDIUM]: Port allocation and worker placement read-then-write without locking. Concurrent creates on same node can allocate same ports or overcommit resources. Docker rejects at runtime but UX is broken. Files: `gameserver.go:84-117`, `gameserver_ports.go:120-160`, `dispatcher.go:101-115`

- **F9** `[DEFERRED]` — **Settings architecture** [LOW]: Flat key-value settings with env override pattern is all-or-nothing global; no per-node settings support (except port range which has a special case). No caching, no schema versioning. Manageable now but design debt for multi-node. Files: `settings.go`, `settings_api.go:143-156`

---

## Completed

### Architecture Findings

- **F3** `[DONE]` — **Webhooks insufficient for business automation**: Replaced single global webhook config with multi-endpoint system. 12 event types, per-endpoint event filtering (glob patterns), HMAC signing, persistent delivery with 24h retry window, enriched payloads with actor token ID, versioned payloads, delivery history API, full CRUD API at `/api/webhooks`, UI management. Commits: `3012d49`, `bc03201`, `d94f97e`, `bad3ed0`, `1951255`, `4148772`, `7cfba56`

### Pass 2 (structural refactors)
- **Task 1** `[DONE]` — Settings getter/setter helpers (`ab79a27`): 481→380 lines, extracted `getInt`/`getBool`/`getString`/`setInt`/`setBool`
- **Task 2** `[DONE]` — Settings API table-driven update (`c6a44e9`): 371→230 lines, 14 case blocks → `settingDefs` map
- **Task 3** `[DONE]` — Page settings handler dedup (`2221a5a`): 774→648 lines, extracted `saveIntHandler`/`boolToggleHandler`
- **Task 4** `[DONE]` — SQL column constants (`fbd86e6`): `gameserverColumns` and `workerNodeColumns` eliminate duplication
- **Task 5** `[DONE]` — Extract WorkerNodeService (`51a160b`): 5 worker-node pass-throughs moved from SettingsService to dedicated service
- **Task 6** `[DONE]` — Split gameserver.go (`ecd7787`): 1224→452 lines, split into 5 files (CRUD, lifecycle, ports, migration, inspect)
- **Task 7** `[DONE]` — Split local.go (`9ed2b31`): 825→301 lines, split into 4 files (core, direct fileops, sidecar fileops, backup)
- **Task 8** `[DONE]` — Split serve.go: 625→378 lines, worker agent/gRPC/heartbeat extracted to `serve_worker.go`, service init extracted to `initServices()`

### Pass 1 (lint-style fixes)
- `auth.go`: Removed 10 redundant comments
- `status.go`: Removed dead code
- `agent.go`: Removed dead code + unused import
- `grpc_auth.go`: Trimmed 1 redundant comment
- `remote.go`: Removed 2 redundant comments
- `settings_api.go`: Fixed struct alignment
- `page_settings.go`: Removed 12 redundant comments
- `serve.go`: Fixed import ordering
- `client.go`: Removed dead function
