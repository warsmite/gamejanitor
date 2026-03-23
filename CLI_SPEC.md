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
| `--config` | — | Path to YAML config file (see CONFIG_SPEC.md) |
| `--controller` | `true` | Enable the API server and orchestrator |
| `--worker` | `true` | Enable the local Docker worker that manages containers on this machine |
| `--port` | `8080` | Port for the HTTP API and web UI |
| `--bind` | `127.0.0.1` | Address to listen on (`0.0.0.0` for all interfaces) |
| `--data-dir` | `/var/lib/gamejanitor` | Where gamejanitor stores its database, backups, and config |
| `--sftp-port` | `2222` | Port for the built-in SFTP file server (0 to disable) |
| `--grpc-port` | `9090` | Port for worker nodes to connect to this controller (0 to disable) |
| `--web-ui` | `true` | Serve the web dashboard (ignored when `--controller=false`) |
| `--controller-address` | — | Address of the controller to register with as a worker (e.g. `10.0.0.1:9090`) |
| `--worker-id` | hostname | Unique name for this worker node |
| `--worker-token` | — | Auth token for this worker to connect to the controller (or `GJ_WORKER_TOKEN`) |

All flags can also be set in the config file. Flags override config file values. Config file is optional — newbies don't need one.

**Startup validation:**
- If `auth_enabled` is true (via config file settings) but no tokens exist in DB → hard error with message: "auth is enabled but no tokens exist — create one first: `gamejanitor token create --type admin`"
- If S3 configured but unreachable → hard error
- If Docker not accessible → hard error with helpful message (suggest `usermod -aG docker` or `sudo`)

**Deployment modes:**
- **Newbie/power user:** `gamejanitor serve` — no config file, no flags, everything works
- **Business controller:** `gamejanitor serve --config /etc/gamejanitor/config.yaml --worker=false`
- **Business worker node:** `gamejanitor serve --config /etc/gamejanitor/config.yaml --controller=false`

**Business deployment flow:**
```bash
# 1. Deploy config file via Ansible/Terraform
# 2. Create admin token (offline, direct DB access)
gamejanitor token create --type admin --data-dir /var/lib/gamejanitor
# → outputs gj_abc123... (save this)

# 3. Start the server
gamejanitor serve --config /etc/gamejanitor/config.yaml

# 4. Configure operational settings via API
curl -X PATCH http://controller:8080/api/settings \
  -H "Authorization: Bearer gj_abc123..." \
  -d '{"max_backups": 5}'
```

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
gamejanitor token create --name <name> --type admin|worker [--data-dir /var/lib/gamejanitor]
```
Offline token creation — direct DB access, no running server needed. Used to create the first admin token before starting with auth enabled. Also used to create worker tokens for multi-node deployment. Uses `--data-dir` to find the database (defaults to `/var/lib/gamejanitor`).

**Idempotent:** if a token with the given name already exists, exits 0 silently. This makes it safe to use in `ExecStartPre` for systemd/NixOS declarative deployments. The raw token is only printed on first creation.

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

## Config File

Optional YAML config file for infrastructure settings and initial operational state. See `CONFIG_SPEC.md` for full specification.

Newbies don't need one. Power users may use one for S3 config. Businesses deploy one via IaC.

```bash
# Generate a starter config file
gamejanitor init [--profile newbie|business]
```

Generates a `gamejanitor.yaml` in the current directory with sensible defaults for the profile:

**`newbie`** (default): minimal config, comments explaining each option.

**`business`**: auth enabled, require limits on, rate limiting on, S3 section with placeholder values, comments for multi-node setup.

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
