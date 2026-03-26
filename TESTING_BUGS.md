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

## PortMode defaults to empty string, skipping auto-allocation — FIXED
- **Fixed:** `CreateGameserver` now treats empty `PortMode` as `"auto"`.

## GameserverIDFromContainerName fails to reject update/fileops containers — FIXED
- **Fixed:** Changed `strings.Contains(id, "-update-")` to `strings.HasPrefix(id, "update-")` (and similar for other prefixes) in `naming/naming.go`.

## runBackup panics on nil worker (missing nil check) — FIXED
- **Fixed:** Added nil check on `w` in `backup.go` — calls `failBackup` if worker unavailable. Same fix in `stats_poller.go`.

## Sidecar file ops don't reject path traversal — FIXED
- **Fixed:** Added `sidecarPath` validation in `LocalWorker` before delegating to sidecar. Tests unskipped in `worker/local_test.go`.

## Sidecar file ops fail with Permission denied on Docker volumes — FIXED
- **Fixed:** Sidecar container now runs as root. Tests unskipped in `worker/local_test.go`.

## Event bus silently drops events under load — FIXED
- **Fixed:** Added `slog.Warn` log when events are dropped in `EventBus.Publish()` (`controller/eventbus.go`).

## ReadyWatcher goroutines have no explicit per-gameserver shutdown — FIXED
- **Fixed:** `Stop()` and `DeleteGameserver()` in lifecycle now call `s.readyWatcher.Stop(id)` explicitly.

## Multiple WorkerFor call sites lack nil checks — FIXED
- **Fixed:** All `WorkerFor` call sites now check for nil and return a clear "worker unavailable" error. Covers: lifecycle.go (Start, Stop, UpdateServerGame, Reinstall), gameserver.go (DeleteGameserver), inspect.go, console.go, file.go, backup.go (runRestore), sftp/file_operator.go.

## CPUEnforced always overwritten to false on any update — FIXED
- **Fixed:** `CPUEnforced` is now only updated when `CPULimit` is also being changed (they're semantically coupled). Test unskipped in `controller/gameserver/update_merge_test.go`.

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
