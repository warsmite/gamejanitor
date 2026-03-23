# CLI Specification

## Principles

- **Newbie-optimized defaults** — `gamejanitor serve` works out of the box with zero config
- **Name-or-ID everywhere** — commands accept gameserver names, not just UUIDs
- **Consistent patterns** — same flags and output style across all commands
- **`--json` on everything** — human-readable tables by default, JSON for scripting
- **No required flags** — sensible defaults for everything, prompt interactively where needed

## Server Mode

```
gamejanitor serve [flags]
```

Starts the gamejanitor server. Both controller and worker are enabled by default — a single command runs everything for newbies.

| Flag | Default | Description |
|---|---|---|
| `--controller` | `true` | Run the API/orchestrator |
| `--worker` | `true` | Run the local Docker worker |
| `--port` | `8080` | HTTP API port |
| `--bind` | `127.0.0.1` | Bind address |
| `--data-dir` | `/var/lib/gamejanitor` | Data directory |
| `--sftp-port` | `2222` | SFTP port (0 to disable) |
| `--grpc-port` | `9090` | gRPC port for multi-node (0 to disable) |
| `--no-ui` | `false` | Disable web UI, API-only |
| `--connect` | — | Controller gRPC address (worker-only mode) |
| `--worker-id` | hostname | Worker node ID |
| `--worker-token` | — | Worker auth token (or `GJ_WORKER_TOKEN`) |

**Deployment modes:**
- **Newbie/power user:** `gamejanitor serve` — both flags default true, everything runs
- **Business controller:** `gamejanitor serve --worker=false` — API + orchestration only, no local Docker
- **Business worker node:** `gamejanitor serve --controller=false --connect controller:9090 --worker-token gj_...` — Docker agent only, registers with controller

## Setup Commands

```
gamejanitor install [--systemd|--launchd]
```
Installs gamejanitor as a system service. Generates service file, enables and starts it. Detects init system if flag not specified.

```
gamejanitor update
```
Self-updates the gamejanitor binary to the latest release.

```
gamejanitor token create --type admin|worker
```
Offline token creation — direct DB access, no running server needed. Used for initial setup before auth is enabled.

```
gamejanitor gen-worker-cert <worker-id>
```
Generates TLS certificate for a worker node. Outputs paths to cert, key, and CA files.

## Cluster Management

For operators managing multiple gamejanitor deployments.

```
gamejanitor cluster add <name> --address <url> --token <token>
gamejanitor cluster use <name>
gamejanitor cluster list
gamejanitor cluster remove <name>
gamejanitor cluster current
```

Stored in `~/.gamejanitor/clusters.yaml`. Token stored per-cluster. Current cluster remembered between commands. All API client commands use the current cluster's address and token.

If no cluster configured, defaults to `http://localhost:8080` with no token (newbie mode).

## Profiles

```
gamejanitor settings apply-profile <profile>
```

Applies a preset configuration profile:

**`newbie`** (default state):
- Auth disabled
- Localhost bypass on
- Require limits off
- Rate limiting off

**`business`**:
- Auth enabled (generates and displays admin token)
- Require memory/cpu/storage limits on
- Rate limiting on
- Prompts for S3 backup config if not set

## Gameservers

### CRUD

```
gamejanitor gameservers list [--game <game-id>] [--status <status>] [--limit N] [--json]
gamejanitor gameservers get <name-or-id> [--json]
gamejanitor gameservers create --name <name> --game <game-id> [flags] [--json]
gamejanitor gameservers update <name-or-id> [flags] [--json]
gamejanitor gameservers delete <name-or-id> [--force]
```

Create flags:
| Flag | Default | Description |
|---|---|---|
| `--name` | required | Server name |
| `--game` | required | Game ID |
| `--memory` | game default | Memory limit (e.g. `4g`, `2048`, `512m`) |
| `--cpu` | 0 (unlimited) | CPU limit (cores) |
| `--storage` | 0 (unlimited) | Storage limit (MB) |
| `--port-mode` | `auto` | `auto` or `manual` |
| `--node` | auto-placed | Specific node ID |
| `--node-tags` | none | Required node tags (comma-separated) |
| `--env` | — | Environment overrides (`KEY=VALUE`, repeatable) |
| `--auto-restart` | false | Enable auto-restart on crash |

Update flags: same as create, all optional. Only provided flags are changed.

### Lifecycle (short form)

```
gamejanitor start <name-or-id>
gamejanitor stop <name-or-id>
gamejanitor restart <name-or-id>
gamejanitor update-game <name-or-id>
gamejanitor reinstall <name-or-id>
gamejanitor migrate <name-or-id> --node <target-node>
```

### Info

```
gamejanitor status [name-or-id]
```
Without argument: cluster overview (all gameservers with status, resource usage).
With argument: detailed status for one gameserver (status, container info, query data, stats).

```
gamejanitor logs <name-or-id> [--tail N] [--follow]
```
`--tail 100` is default. `--follow` streams live logs.

```
gamejanitor command <name-or-id> <command...>
```
Sends a console command, prints output.

## Backups

```
gamejanitor backups list <gameserver> [--json]
gamejanitor backups create <gameserver> [--name <name>]
gamejanitor backups restore <gameserver> <backup-id>
gamejanitor backups download <gameserver> <backup-id> [--output <file>]
gamejanitor backups delete <gameserver> <backup-id>
```

Create returns immediately (async). Restore returns immediately (async). Both print the backup ID and tell the user to check events for completion.

## Schedules

```
gamejanitor schedules list <gameserver> [--json]
gamejanitor schedules create <gameserver> --type <type> --cron <expr> [--name <name>] [--payload <json>] [--enabled]
gamejanitor schedules update <schedule-id> [--cron <expr>] [--enabled true|false] [--name <name>]
gamejanitor schedules delete <schedule-id>
```

Types: `restart`, `backup`, `command`, `update`.

## Webhooks

```
gamejanitor webhooks list [--json]
gamejanitor webhooks create --url <url> [--events <glob,...>] [--secret <secret>] [--description <desc>]
gamejanitor webhooks update <id> [--url] [--events] [--secret] [--enabled true|false]
gamejanitor webhooks delete <id>
gamejanitor webhooks test <id>
gamejanitor webhooks deliveries <id> [--state pending|delivered|failed] [--limit N]
```

Default events: `["*"]` (all). Secret is user-provided for HMAC-SHA256 signing — both sides need to know it.

## Events

```
gamejanitor events [--type <glob>] [--gameserver <name-or-id>] [--limit N] [--json]
gamejanitor events --follow [--types <glob,...>]
```

Without `--follow`: queries event history from DB.
With `--follow`: streams live events via SSE. Same `--types` filtering as the API.

## Tokens

```
gamejanitor tokens list [--json]
gamejanitor tokens create --name <name> [--scope admin|custom|worker] [--permissions <perm,...>] [--gameservers <id,...>] [--expires <duration>]
gamejanitor tokens delete <id>
```

Merged command group — admin, custom, and worker tokens all created here. `--scope admin` pre-selects all permissions. `--scope worker` pre-selects `worker.connect`. Default scope: `custom`.

Shows the raw token once on creation. Warns user to save it.

## Workers

```
gamejanitor workers list [--json]
gamejanitor workers get <id> [--json]
gamejanitor workers set <id> [--memory <MB>] [--cpu <cores>] [--storage <MB>] [--port-range <start>-<end>] [--tags <tag,...>]
gamejanitor workers clear <id> [--limits] [--port-range] [--tags]
gamejanitor workers cordon <id>
gamejanitor workers uncordon <id>
```

`set` is a unified command for all worker configuration. `clear` removes specific settings.

## Settings

```
gamejanitor settings [--json]
gamejanitor settings set <key> <value>
gamejanitor settings apply-profile <newbie|business>
```

Without subcommand: display all settings with current values and whether they're ENV-controlled.

## Games

```
gamejanitor games list [--json]
gamejanitor games get <id> [--json]
```

List returns basic info (id, name, image, recommended memory). Get returns full definition including default ports, env vars with descriptions/options/types, ready pattern, capabilities.

## Output

**Table format** (default): human-readable aligned columns.
```
$ gamejanitor gameservers list
NAME          GAME              STATUS    MEMORY   CPU    NODE
my-mc-server  minecraft-java    running   4 GB     2.0    node-1
test-ark      ark-survival      stopped   8 GB     4.0    node-2
```

**JSON format** (`--json`): machine-readable, same envelope as API.
```json
{"status":"ok","data":[...]}
```

**Status colors**: green=running, yellow=installing/starting, red=error, gray=stopped. Respects `NO_COLOR` env var.

## Error Messages

Same principle as API: user-facing errors are clear and actionable. Internal errors show a generic message with a suggestion to check server logs.

```
$ gamejanitor start nonexistent
Error: gameserver "nonexistent" not found

$ gamejanitor gameservers create --name test --game fake
Error: game "fake" not found. Run 'gamejanitor games list' to see available games.

$ gamejanitor start my-server
Error: cannot connect to gamejanitor at http://localhost:8080
  Is the server running? Start it with: gamejanitor serve
  Or configure a remote cluster: gamejanitor cluster add ...
```
