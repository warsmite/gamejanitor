# Bugs Found During Testing

Bugs discovered while building the test suite. Each entry has a corresponding skipped test that asserts the correct behavior.

Format:
```
## <Short description>
- **Test:** `TestName` in `path/to/file_test.go`
- **Expected:** what should happen
- **Actual:** what happens instead
- **Severity:** blocks release / should fix / cosmetic
- **Notes:** additional context, links to MEMORY.md entries, etc.
```

---

## PortMode defaults to empty string, skipping auto-allocation
- **Test:** `TestGameserver_Create_PortModeDefaultShouldBeAuto` in `service/gameserver_test.go` (to be written in Phase 3)
- **Expected:** When a gameserver is created without explicitly setting `port_mode`, ports should be auto-allocated from the configured port range. The DB default is `'auto'` but Go struct zero value is `""`.
- **Actual:** `CreateGameserver` checks `gs.PortMode == "auto"` — empty string skips allocation entirely. Ports fall through to `applyGameDefaults` which uses the game's raw default ports (e.g., 27015). Two gameservers of the same game get identical host ports and will conflict on start.
- **Severity:** blocks release
- **Notes:** The API handler (`api/handlers/gameservers.go`) never sets `PortMode`. The DB column default `'auto'` only applies on SQL INSERT, but port allocation runs before the INSERT. Fix is likely: treat empty `PortMode` as `"auto"` in `CreateGameserver`, or have the API handler set it explicitly.

## GameserverIDFromContainerName fails to reject update/fileops containers
- **Test:** `TestNaming_GameserverIDFromContainerName_RejectsUpdateContainer` and `_RejectsFileopsContainer` in `naming/naming_test.go`
- **Expected:** `GameserverIDFromContainerName("gamejanitor-update-abc-123")` should return `("", false)` since it's an update container, not a gameserver.
- **Actual:** Returns `("update-abc-123", true)`. After stripping `gamejanitor-` prefix, the remainder is `update-abc-123`. The check `strings.Contains(id, "-update-")` looks for `-update-` with a leading dash, but the remainder starts with `update-` (no leading dash). Same issue for `-fileops-`, `-reinstall-`, `-backup-`.
- **Severity:** should fix
- **Notes:** `naming/naming.go:34`. The StatusManager uses this to map container events to gameservers. Misidentifying update containers could cause spurious status changes. Fix: use `strings.HasPrefix(id, "update-")` instead of `strings.Contains(id, "-update-")`.

## runBackup panics on nil worker (missing nil check) — FIXED
- **Test:** Discovered via `TestBackup_Create_ReturnsInProgressRecord` flaky panic in `service/backup_test.go`
- **Fixed:** Added nil check on `w` in `service/backup.go:115` — calls `failBackup` if worker unavailable.
- **Also fixed:** Same nil worker panic in `service/stats_poller.go:117` — returns false to stop polling.
- **Notes:** Many other `WorkerFor` call sites in the codebase also lack nil checks (console.go, file.go, gameserver_inspect.go, etc). These are synchronous paths so less likely to hit in practice, but should be audited.

## Sidecar file ops don't reject path traversal
- **Test:** `TestWorker_FilePathTraversal` in `worker/local_test.go` (integration, skipped)
- **Expected:** `ReadFile(ctx, vol, "../../etc/passwd")` should return an error.
- **Actual:** Returns the container's `/etc/passwd` contents without error. The sidecar executes `cat` inside an Alpine container, so `../../etc/passwd` resolves to the container's `/etc/passwd` — not a host escape, but reads files outside `/data`.
- **Severity:** should fix
- **Notes:** The direct-access code path has `resolveVolumePath` with `strings.HasPrefix` protection, but the sidecar path (`local_fileops_sidecar.go`) doesn't enforce the same check before passing paths to container exec commands. Fix: validate paths in `LocalWorker` before delegating to either direct or sidecar.

## Sidecar file ops fail with Permission denied on Docker volumes
- **Test:** `TestWorker_VolumeOperations`, `TestWorker_BackupRestoreRoundTrip`, `TestWorker_Rename` in `worker/local_test.go` (integration, skipped)
- **Expected:** Write/mkdir/rename operations should work on Docker volumes.
- **Actual:** `Permission denied` — sidecar container runs as `1001:1001` but Docker volume root directory is owned by `root`.
- **Severity:** should fix
- **Notes:** This is the same underlying issue as the "File delete fails (Docker)" bug in MEMORY.md. Affects all sidecar file operations, not just delete. The direct-access path works but is only available when Docker volume mountpoints are accessible on the host (not when gamejanitor runs inside Docker itself).

## Event bus silently drops events under load
- **Test:** Discovered via flaky `TestReady_BothInstallAndReadyDetectedOnFirstStart` (removed as flaky) and `TestPipeline_StatusDerivedFromLifecycleEvents` (intermittent)
- **Expected:** All lifecycle events (image_pulling → image_pulled → container_creating → container_started → ready) should be received by all subscribers.
- **Actual:** `EventBus.Publish()` uses `select/default` — if a subscriber's 64-element channel buffer is full, the event is silently dropped with no logging or metrics. The StatusSubscriber does a DB read+write per event on single-connection SQLite, so it falls behind during rapid event bursts. When it's writing one status change, subsequent events overflow the buffer and are lost.
- **Severity:** should fix
- **Notes:** `service/broadcast.go:57-66`. This is a production bug, not just a test issue. A user starting multiple gameservers simultaneously would see missing status transitions. Possible fixes: (1) increase buffer size, (2) log when events are dropped, (3) use a blocking publish with timeout, (4) batch DB writes in the subscriber. At minimum, dropped events should be logged so operators can see the problem.

## ReadyWatcher goroutines have no explicit per-gameserver shutdown
- **Test:** Discovered via cleanup races in all tests that call `GameserverService.Start`
- **Expected:** When a gameserver is stopped or deleted, the ReadyWatcher goroutine monitoring its logs should be explicitly stopped.
- **Actual:** The ReadyWatcher relies on the container log stream ending naturally (EOF from Docker). There's a `Stop(gameserverID)` method, but `GameserverService.Stop` never calls it — it just stops the container and hopes the log stream closes. In practice this works because Docker closes the stream when the container exits, but there's a race window between container stop and log stream close where the goroutine is still running and holding references to the DB and worker.
- **Severity:** cosmetic (works in practice, but causes test flakiness and is technically a resource leak)
- **Notes:** `service/ready.go:100-109` has `Stop(gameserverID)` but it's never called from the lifecycle code. `service/gameserver_lifecycle.go` Stop/Delete methods should call `s.readyWatcher.Stop(id)` explicitly. The `StopAll()` is only called on process shutdown.

## Multiple WorkerFor call sites lack nil checks
- **Test:** Discovered via panics in `TestBackup_Create` and `TestPipeline_*` (2 fixed, others remain)
- **Expected:** All callers of `dispatcher.WorkerFor(gameserverID)` should handle nil returns gracefully.
- **Actual:** `WorkerFor` returns nil when the DB lookup fails or the worker isn't registered. Two call sites were fixed (backup.go, stats_poller.go). The remaining unchecked call sites will panic on nil pointer dereference if the worker disconnects at the wrong time:
  - `service/console.go:60` — StreamLogs
  - `service/console.go:91` — SendCommand
  - `service/console.go:113` — ListLogSessions
  - `service/console.go:156` — ReadHistoricalLogs
  - `service/file.go:46,61,76,94,109,132` — all file operations
  - `service/gameserver_inspect.go:23,35,71,85` — inspect, stats, volume size, logs
  - `service/gameserver.go:546` — DeleteGameserver
  - `service/gameserver_lifecycle.go:70,198,292,381` — Start, Stop, UpdateServerGame, Reinstall
  - `service/backup.go:270` — runRestore
- **Severity:** should fix
- **Notes:** The synchronous paths (console, file, inspect) are less likely to hit in practice because the worker was just used to start the container. But worker disconnection between operations is possible in multi-node setups. Fix: each call site should check for nil and return a clear "worker unavailable" error instead of panicking.

---

# API Surface Issues

Things that aren't bugs but caused confusion during test development. These signal unclear interfaces that could be improved.

## ValidateToken returns a single value, not (token, error)
- **Location:** `service/auth.go:47`
- **Issue:** `ValidateToken(rawToken string) *models.Token` returns nil for both "invalid token" and "expired token" — the caller can't distinguish between the two, and the lack of an error return is unusual for a Go function that can fail.
- **Suggestion:** Consider `(token, error)` return to distinguish invalid vs expired vs DB failure. Or at minimum, this is worth a doc comment explaining that nil means "not valid for any reason."

## HasPermission and IsAdmin are package-level functions, not AuthService methods
- **Location:** `service/auth.go:255`, `service/auth.go:262`
- **Issue:** `HasPermission(token, gameserverID, permission)` and `IsAdmin(token)` are standalone functions, not methods on `AuthService`. Every other auth operation is on `AuthService`. A caller naturally writes `svc.AuthSvc.HasPermission(...)` and gets a compile error.
- **Suggestion:** Either make them methods on `AuthService` for consistency, or document the split (pure functions vs stateful operations).

## Error messages use env var labels, not keys
- **Location:** `service/gameserver.go` — `validateRequiredEnv`
- **Issue:** When a required env var is missing, the error says `"Required Variable is required"` (using the `Label` field) not `"REQUIRED_VAR is required"` (using the `Key` field). The label is user-friendly for the UI but confusing in API errors and logs where the caller works with keys.
- **Suggestion:** Include both: `"REQUIRED_VAR (Required Variable) is required"` — key for programmatic use, label for human context.
