# Prioritized TODO List

Odd numbers: Agent A (current session)
Even numbers: Agent B

## Release Blockers

1. ~~Settings page broken — Fixed: read-only config keys rejected by PATCH handler. Moved read-only config to status endpoint.~~
2. ~~Documentation (#18) — Not relevant to this repo.~~
3. ~~File delete fails (Docker) — Fixed: sidecar ran as UID 1001, couldn't delete root-owned files. Now runs as root with chown after writes.~~
4. Recovery audit (#68) — Thorough audit of all recovery paths and edge cases before release.
5. Missing ON DELETE CASCADE — Schedules and backups tables have no CASCADE. App code manually deletes, orphans possible on failure.
6. ~~Backup delete ordering — DB record now deleted before store file. Orphaned files are harmless.~~

## Correctness / Data Integrity

7. ~~Settings have no validation — Fixed: added validate package with shared helpers, per-key validators on settings, Validate() methods on models, wired into all service write paths.~~
7b. Remove WorkerNodeUpdate struct — Only update struct in the codebase. Every other update path uses the model directly or individual params. Align with existing patterns.
8. ~~Silent JSON unmarshal errors — Tags refactored to Labels type (map[string]string) with sql.Scanner/Valuer. mod.go parseEnv now logs errors.~~
9. No CHECK constraints on status columns — Any string accepted in status columns.
10. ~~Token creation doesn't validate gameserver IDs — CreateCustomToken now validates all IDs exist before creating token.~~
11. Webhook delivery aborts on first error — If one endpoint fails, others don't get deliveries for that event.
12. Bind address vs dial-back mismatch — Worker bound to 127.0.0.1 reports LAN IP for dial-back. Detect and warn on startup.

## API Consistency / Quality

13. Schedule/backup list permission mismatch — Listing requires create permission. Need view/read permission.
14. No pagination on backup list — backupHandlers.List doesn't call parsePagination.
15. No pagination cap — Clients can request ?limit=1000000. Need sane max (100-200).
16. Health endpoint not structured — GET /health returns plain text, should return JSON envelope.
17. ~~Worker token listing inefficient — Fixed: added ListTokensByScope with SQL WHERE, handler calls it directly.~~
18. ~~Cordon uses inconsistent pattern — Resolved: single PATCH /api/workers/:id replaces all one-off endpoints.~~
19. No batch gameserver query by IDs — Need GET /api/gameservers?ids=a,b,c for customer dashboards.
20. Webhook create doesn't validate URL reachability — Unreachable URLs silently queue and fail.

## Multi-Node / Orchestrator

21. Runtime switch detection — Warn on startup if DB has gameservers but runtime can't find containers.
22. Bedrock query poller bug — Query poller not starting for Minecraft Bedrock despite promote to running.
23. Custom game defs for multi-node (#76) — Local override dir doesn't scale. Need distribution mechanism.
24. gRPC port UX for newbies — Port conflicts could confuse newbies. Consider auto-pick or better errors.
25. Idempotent token create — CreateWorkerToken should return existing token if name matches. Required for NixOS ExecStartPre.
26. Dockerfile (#69) — Gamejanitor-in-Docker needs testing and a Dockerfile.

## Config / Settings

27. Env override vs per-node settings (F9) — Settings architecture rework, boilerplate reduction.
28. memory_limit_mb=0 should mean unlimited — applyGameDefaults overrides it.
29. Hosted mode defaults — Config setting that inits sane defaults (auth_enabled=true etc) for business deployments.
30. Env vs file config confusion (#29) — Per-game docs on which settings are env-var controlled vs config files.

## Features

31. Mod system: dep resolution + auto-updates — Dependency resolution and auto-update checking.
32. Mod system: Valheim BepInEx/Thunderstore — Needs BepInEx framework + Thunderstore API.
33. Mod system: 7D2D uMod — Game.yaml doesn't have mods config yet.
34. Rust WebRCON — Console/command support for Rust gameservers.

## CLI

35. Define CLI scope — Server mode + client mode. Current CLI is a testbed.
36. CLI context system — kubectl-style context switching between controllers.
37. Service install command — gamejanitor install for systemd/launchd.
38. Docker connection error UX — User-friendly messages when Docker/Podman not found.

## Web UI

39. Define UI scope — Admin dashboard for all archetypes.
40. Disableable web UI (#47) — --no-ui flag for API-only deployments.
41. Onboarding experience — First-run setup flow for new users.
42. White-label / customer UI (#50) — Probably not needed.

## Game Definitions

43. Game env vars lack "required for hosting" concept — No way to distinguish engine requirement from legal agreement.

## Security / TLS

44. mTLS production hardening — Bring-your-own CA docs, cert expiry + rotation, CA key encryption.

## Infrastructure

45. Per-IP port binding (#53) — Multiple IPs per node, gameservers on default game ports.
46. Worker SFTP config clarity (#74) — Docs on controller-proxied vs direct-to-worker SFTP.
47. File manager improvements (#30-31) — Zip/unzip for mod packs, review 10MB download limit.
48. Rename docker/ package — Consider container/ or runtime/. Works with Podman too.

## Process Runtime

49. Process runtime: systemd-run — Replace bwrap die-with-parent with systemd-run scope.
50. Process runtime: fallback UX — Offer process mode when no Docker/Podman found.

## Testing

51. Comprehensive testing — Integration tests for lifecycle, events, webhooks, placement, migration, backup/restore, auth.
52. Mac/WSL support (#23) — Test and document.

## Self-Update / Deployment

53. Self-update CLI command — gamejanitor update for self-updating.
54. Webhook IaC/CLI support — CLI provisioning for webhook endpoints.

## Low Priority

55. Resource allocation race condition — Verify placementMu mutex scope for concurrent requests.
56. Worker disconnect during long operations — Backups continue against dead workers undetected.
57. Event bus has no flow control — Slow subscriber can block others.

## Extreme Low Priority

58. MC Java multi-JRE image is ~1GB — Consider separate images.
59. Phone/Android runtime — Box64 cross-compile for ARM, Termux support.
