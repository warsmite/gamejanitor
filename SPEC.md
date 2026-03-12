# Gamejanitor Specification

Local game server hosting tool. Single node, Docker-based, with CLI and WebUI.

## Trust Model

Single trusted user, local network only. The WebUI has no authentication. Security focus is on preventing the user from accidentally shooting themselves in the foot (path traversal, clear error messages), not on defending against malicious actors.

## Goals

- **Simple to set up** — one binary, SQLite, Docker. No complex dependencies.
- **Simple to configure** — NixOS module for declarative config, or WebUI/CLI for interactive use.
- **Fully featured** — everything you need to run game servers: lifecycle, console, files, backups, scheduling.
- **Consistent image API** — every game image implements the same script interface, making it easy to add new games.
- **Agent-friendly CLI** — all commands support `--json`, consistent output format, composable.

## Tech Stack

| Component | Choice |
|---|---|
| Language | Go |
| Database | SQLite (WAL mode) |
| WebUI | HTMX + Alpine.js + Tailwind CSS |
| Console streaming | SSE (Server-Sent Events) |
| Code editor | CodeMirror (for file editor) |
| CLI framework | Cobra |
| Docker | Docker Engine API (Go SDK) |
| Server query | [gsq](https://github.com/0xkowalskidev/gsq) |
| Build/deps | Nix flake |
| Container registry | registry.0xkowalski.dev |

### SQLite Configuration

SQLite must be configured with WAL mode for concurrent access from multiple goroutines (status manager, scheduler, HTTP handlers). Use a single `*sql.DB` instance with appropriate pool settings.

### Logging

Structured logging with Go's `slog` package from Phase 1. All key operations and state transitions must be logged:
- Gameserver status transitions (e.g. `[gameserver:abc123] status: started -> running`)
- Docker operations (pull, create, start, stop, remove)
- Scheduler task execution
- Errors with context (what failed, why, which gameserver)

Log levels: `debug` for verbose output (Docker API calls, GSQ poll results), `info` for significant events (status changes, backup created), `warn` for recoverable issues (GSQ poll failed, image pull failed but cached image exists), `error` for failures.

Toggle verbose logging via `DEBUG=1` environment variable.

## Project Structure

```
gamejanitor/
├── flake.nix
├── go.mod / go.sum
├── SPEC.md
├── cmd/
│   ├── gamejanitor/
│   │   └── main.go              # Entrypoint: initializes cobra root command
│   └── cli/
│       ├── root.go              # Cobra root, --json flag, --api-url flag. `serve` starts daemon, all else are API clients.
│       ├── serve.go             # serve command: starts HTTP server, scheduler, docker event watcher
│       ├── games.go             # games list|get|create|update|delete
│       ├── gameservers.go       # gameservers list|get|create|update|delete
│       ├── actions.go           # gameservers start|stop|restart|update-game|reinstall
│       ├── console.go           # gameservers logs|send
│       ├── schedules.go         # schedules list|create|update|delete
│       └── backups.go           # backups list|create|restore|delete
├── internal/
│   ├── config/
│   │   └── config.go            # App config: listen addr, db path, data dir, docker socket
│   ├── db/
│   │   ├── db.go                # SQLite connection, migration runner
│   │   └── migrations/          # Numbered SQL migration files
│   ├── models/
│   │   ├── game.go
│   │   ├── gameserver.go
│   │   ├── schedule.go
│   │   └── backup.go
│   ├── docker/
│   │   └── docker.go            # Docker client: containers, volumes, exec, logs, events, stats
│   ├── service/
│   │   ├── game.go              # Game CRUD
│   │   ├── gameserver.go         # Gameserver CRUD + lifecycle (start/stop/restart/update/reinstall)
│   │   ├── console.go           # Log streaming, command sending
│   │   ├── files.go             # File browser/editor via docker cp/exec
│   │   ├── backup.go            # Backup/restore (tar volumes via docker exec)
│   │   ├── scheduler.go         # Cron scheduler for recurring tasks
│   │   ├── query.go             # GSQ integration for gameserver querying
│   │   └── status.go            # Status manager: Docker events + GSQ polling
│   └── web/
│       ├── router.go            # HTTP router (chi). /api/* returns JSON, /* returns HTML.
│       ├── handlers/            # Each handler serves both JSON (/api/) and HTML (/) responses
│       │   ├── dashboard.go     # Dashboard with gameserver overview
│       │   ├── games.go         # Game management pages
│       │   ├── gameservers.go   # Gameserver management pages
│       │   ├── console.go       # Console page with SSE stream
│       │   ├── files.go         # File browser/editor pages
│       │   ├── schedules.go     # Schedule management pages
│       │   └── backups.go       # Backup management pages
│       ├── templates/
│       │   ├── layout.html
│       │   ├── dashboard.html
│       │   ├── games/
│       │   ├── gameservers/
│       │   ├── console/
│       │   ├── files/
│       │   ├── schedules/
│       │   └── backups/
│       └── static/              # htmx.js, alpine.js, tailwind, codemirror
├── images/
│   ├── base/
│   │   ├── Dockerfile
│   │   └── scripts/
│   │       ├── entrypoint.sh
│   │       ├── install-server
│   │       ├── update-server
│   │       ├── start-server
│   │       ├── stop-server
│   │       ├── send-command
│   │       └── health-check
│   ├── minecraft-java/
│   │   ├── Dockerfile
│   │   ├── scripts/
│   │   └── defaults/
│   ├── rust/
│   │   ├── Dockerfile
│   │   ├── scripts/
│   │   └── defaults/
│   └── ...
└── nixos/
    └── module.nix
```

---

## Data Model

### games

Game definitions. Seeded with defaults on first run, but full CRUD so users can add custom games or tweak existing ones.

| Column | Type | Description |
|---|---|---|
| id | TEXT PK | Slug: `minecraft-java`, `rust` |
| name | TEXT | Display name: `Minecraft: Java Edition` |
| image | TEXT | Docker image: `registry.0xkowalski.dev/gamejanitor/minecraft-java` |
| default_ports | JSON | `[{"name":"game","port":25565,"protocol":"tcp"}]` |
| default_env | JSON | Env var definitions with defaults and UI hints. See [Environment Config](#environment-config). |
| min_memory_mb | INTEGER | Minimum recommended memory |
| min_cpu | REAL | Minimum recommended CPU cores |
| gsq_game_slug | TEXT | Slug for `gsq.Query()`, nullable if game doesn't support query |
| disabled_capabilities | JSON | Capabilities this game does NOT support. See [Capabilities](#capabilities). |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### gameservers

Game server instances. Each gameserver maps to one Docker container and one Docker volume.

| Column | Type | Description |
|---|---|---|
| id | TEXT PK | UUID |
| name | TEXT | User-given name |
| game_id | TEXT FK | References games.id |
| ports | JSON | `[{"name":"game","host_port":25565,"container_port":25565,"protocol":"tcp"}]` |
| env | JSON | User env var overrides: `{"GAMEMODE":"creative","MAX_PLAYERS":"50"}`. Merged with game defaults at container start. |
| memory_limit_mb | INTEGER | Docker memory limit |
| cpu_limit | REAL | Docker CPU limit |
| auto_start | BOOLEAN | Start on Gamejanitor startup |
| container_id | TEXT | Docker container ID, nullable |
| volume_name | TEXT | Docker volume name: `gamejanitor-<gameserver-id>` |
| status | TEXT | See [Status Lifecycle](#status-lifecycle) |
| created_at | DATETIME | |
| updated_at | DATETIME | |

### schedules

Recurring tasks attached to gameservers.

| Column | Type | Description |
|---|---|---|
| id | TEXT PK | UUID |
| gameserver_id | TEXT FK | References gameservers.id |
| name | TEXT | User-given name for the task |
| type | TEXT | `restart`, `backup`, `command`, `update` |
| cron_expr | TEXT | Standard cron expression |
| payload | JSON | Type-specific data, e.g. `{"command":"save-all"}` for command type |
| enabled | BOOLEAN | |
| last_run | DATETIME | |
| next_run | DATETIME | Precomputed for display |
| created_at | DATETIME | |

### backups

Backup records. Actual backup files stored on disk.

| Column | Type | Description |
|---|---|---|
| id | TEXT PK | UUID |
| gameserver_id | TEXT FK | References gameservers.id |
| name | TEXT | Auto-generated or user-given |
| file_path | TEXT | Path to tar.gz on host |
| size_bytes | INTEGER | |
| created_at | DATETIME | |

---

## Environment Config

All game configuration is done through environment variables passed to the Docker container. The image handles translating env vars into whatever the game needs (config files, CLI args, etc.). Gamejanitor doesn't need to know about config file formats.

On gameserver start, Gamejanitor:
1. Takes the game's `default_env` field defaults
2. Merges with the gameserver's `env` overrides (user values win)
3. For `system` fields, overrides with the correct value (e.g. port fields set from gameserver port mapping)
4. Injects `MEMORY_LIMIT_MB` from the gameserver's `memory_limit_mb` setting
5. Passes all merged env vars to the Docker container

Users can always edit game config files directly via the file browser. The env vars are for the structured UI and for ensuring system-managed values stay correct.

### Field Schema

Each entry in `default_env` is a JSON object:

| Property | Required | Description |
|---|---|---|
| `key` | yes | Env var name passed to Docker |
| `default` | yes | Default value (always a string) |
| `label` | no | Display label in WebUI. If absent, defaults to `key`. |
| `type` | no | `string` (default), `number`, `boolean`, `select`. Controls WebUI input widget. |
| `options` | for select | Array of valid values for dropdown |
| `system` | no | If true, user cannot edit. Gamejanitor sets the value automatically. Hidden from UI. |

### Example: Minecraft Java

```json
[
  {"key": "EULA",        "default": "false", "label": "Accept Minecraft EULA", "type": "boolean"},
  {"key": "GAMEMODE",    "default": "survival",              "label": "Game Mode",        "type": "select", "options": ["survival", "creative", "adventure", "spectator"]},
  {"key": "MAX_PLAYERS", "default": "20",                    "label": "Max Players",      "type": "number"},
  {"key": "DIFFICULTY",  "default": "normal",                "label": "Difficulty",        "type": "select", "options": ["peaceful", "easy", "normal", "hard"]},
  {"key": "MOTD",        "default": "A Gamejanitor Server",  "label": "Message of the Day"},
  {"key": "PVP",         "default": "true",                  "label": "PvP",              "type": "boolean"},
  {"key": "SERVER_PORT", "default": "25565", "system": true}
]
```

The Minecraft image's entrypoint reads these env vars and writes `server.properties` accordingly. `SERVER_PORT` is set by Gamejanitor from the container port in the port mapping.

### Example: Rust

```json
[
  {"key": "SERVER_MAXPLAYERS", "default": "50",                       "label": "Max Players",    "type": "number"},
  {"key": "SERVER_HOSTNAME",   "default": "Gamejanitor Rust Server",  "label": "Server Name"},
  {"key": "SERVER_WORLDSIZE",  "default": "3000",                     "label": "World Size",     "type": "number"},
  {"key": "RCON_PASSWORD",     "default": "changeme",                 "label": "RCON Password"},
  {"key": "SERVER_PORT",       "default": "28015", "system": true},
  {"key": "RCON_PORT",         "default": "28016", "system": true}
]
```

Rust's `start-server` script reads these env vars and translates them to CLI args: `+server.maxplayers ${SERVER_MAXPLAYERS} +server.hostname "${SERVER_HOSTNAME}" ...`

### System-Managed Fields

Fields with `"system": true` are set by Gamejanitor automatically and hidden from the user. Examples:
- **Port fields**: set from the gameserver's port mapping (container_port value). The container always listens on the default port; Docker maps it to whatever host port the user chose.
- **`MEMORY_LIMIT_MB`**: Always injected by Gamejanitor from the gameserver's `memory_limit_mb`. Not defined in `default_env` — Gamejanitor adds it automatically. Images can use this to set JVM heap, etc.

---

## Capabilities

All capabilities are **enabled by default**. The `disabled_capabilities` field on the game model lists what a game does NOT support. This means adding a new game with no `disabled_capabilities` gets all features, and you only need to exclude things explicitly.

Capability list:

| Capability | Description |
|---|---|
| `console_read` | Can stream server console output |
| `console_send` | Can send commands to the server |
| `query` | Supports GSQ server querying (player list, etc.) |
| `mod_support` | Supports mod/plugin installation |
| `update` | Server can be updated in-place |
| `save_command` | Server has a save command (for pre-backup saves) |

The WebUI checks capabilities before rendering UI sections. The CLI includes capabilities in `--json` output so agents know what operations are available.

---

## Status Lifecycle

```
                              ┌──────────┐
                              │ stopped  │
                              └────┬─────┘
                                   │ start
                              ┌────▼─────┐
                              │ pulling  │  (image pull in progress)
                              └────┬─────┘
                                   │ pull complete
                              ┌────▼─────┐
                              │ starting │  (container created & starting)
                              └────┬─────┘
                                   │ Docker says container is running
                              ┌────▼─────┐
                              │ started  │  (container up, game server booting)
                              └────┬─────┘
                                   │ GSQ query succeeds
                              ┌────▼─────┐
                              │ running  │  (game server accepting connections)
                              └────┬─────┘
                                   │ stop
                              ┌────▼─────┐
                              │ stopping │
                              └────┬─────┘
                                   │ container stopped
                              ┌────▼─────┐
                              │ stopped  │
                              └──────────┘

     Any state ───────────────► error (container exited unexpectedly, repeated
                                       GSQ failures after being running, etc.)
```

For games without GSQ support (`query` in `disabled_capabilities`), the status goes directly from `started` → `running` once Docker reports the container is up. No GSQ polling is used.

### Status tracking implementation

**Docker Events Watcher** — a long-lived goroutine subscribes to the Docker events API. On container state change events, it updates the gameserver status in the DB. This is push-based, no polling needed for Docker state.

**GSQ Poller** — when a gameserver reaches `started` and the game supports `query`, a goroutine begins polling GSQ at a configurable interval (default 5s). Once GSQ succeeds, status flips to `running`. After reaching `running`, polling continues at a slower interval (default 30s) to detect game server crashes (container running but game process died). If 5 consecutive GSQ polls fail while in `running` state, status flips to `error`.

**Crash Recovery** — on Gamejanitor startup:
1. Load all gameservers from DB
2. For each gameserver with a non-terminal status (`pulling`, `starting`, `started`, `running`, `stopping`):
   - Query Docker for the actual container state
   - If container is running → set status to `started`, let GSQ poller promote to `running`
   - If container is stopped/gone → set status to `stopped`
   - If container is in error state → set status to `error`
3. For gameservers with `auto_start = true` and status `stopped` → start them

This ensures the DB always converges to reality after a host crash, power loss, or Gamejanitor restart.

**WebUI Status Updates** — the dashboard uses SSE to receive real-time status changes. The status manager broadcasts status changes to all connected SSE clients.

### API Endpoint

```
GET /api/gameservers/:id/status
```

Response:

```json
{
  "status": "running",
  "container": {
    "state": "running",
    "started_at": "2026-03-12T15:00:00Z",
    "memory_usage_mb": 2048,
    "memory_limit_mb": 4096,
    "cpu_percent": 12.5
  },
  "query": {
    "players_online": 3,
    "max_players": 20,
    "players": ["player1", "player2", "player3"],
    "map": "world",
    "version": "1.21.4"
  }
}
```

- `container` is null when status is `stopped`
- `query` is null when GSQ hasn't succeeded yet, game doesn't support query, or server isn't started
- `query` data comes from the last successful GSQ poll (cached), not a live query per request

---

## Docker Image Contract

Every game image extends the base image and implements a standard script interface.

### Base Image

- Based on `ubuntu:24.04` (or similar, needs common libs for game servers)
- Contains: `entrypoint.sh`, stub scripts, common utilities
- Creates a `/data` directory as the working directory (mounted as a Docker volume)
- Runs as a non-root `gameserver` user

### Required Scripts

All scripts live in `/scripts/` and are executable.

| Script | Args | Description |
|---|---|---|
| `install-server` | none | First-time server installation. Downloads binaries, sets up initial config. |
| `start-server` | none | Starts the game server process in the foreground. |
| `stop-server` | none | Graceful shutdown. Sends save/quit commands, waits for process exit. |
| `update-server` | none | Updates game server to latest version. Called while server is stopped. |
| `send-command` | `<command>` | Sends a command to the running server. Method varies by game (FIFO pipe, RCON, etc.). |
| `health-check` | none | Returns exit 0 if the game server process is healthy, 1 otherwise. |

### Environment Variables and Config Files

The image is responsible for translating environment variables into whatever the game needs. On every start, the entrypoint must read env vars and overwrite the relevant values in local config files (e.g. write `GAMEMODE` into `server.properties`, build CLI args from `SERVER_MAXPLAYERS`, etc.). This ensures the env vars passed by Gamejanitor are always the source of truth, even if the user manually edited a config file via the file browser. Env vars win.

### Entrypoint Flow

The entrypoint traps SIGTERM/SIGINT and delegates to `stop-server` for graceful shutdown, since not all game servers handle signals correctly on their own.

```bash
#!/bin/bash
# entrypoint.sh

shutdown() {
    /scripts/stop-server
    wait $SERVER_PID
    exit 0
}
trap shutdown SIGTERM SIGINT

if [ ! -f /data/.installed ]; then
    /scripts/install-server
    touch /data/.installed
fi

# Copy default configs only if they don't already exist
if [ -d /defaults ]; then
    cp -n /defaults/* /data/ 2>/dev/null || true
fi

/scripts/start-server &
SERVER_PID=$!
wait $SERVER_PID
```

### FIFO Pipe for stdin-based Command Sending

Games that accept commands via stdin (e.g. Minecraft) use a FIFO pipe pattern:

**In `start-server`:**
```bash
#!/bin/bash
FIFO=/tmp/cmd-input
mkfifo "$FIFO" 2>/dev/null || true
# Tail the FIFO to keep it open, pipe into the server process
tail -f "$FIFO" | exec java -Xmx${MAX_MEMORY} -jar /data/server.jar nogui
```

**In `send-command`:**
```bash
#!/bin/bash
echo "$1" > /tmp/cmd-input
```

Games using RCON (e.g. Rust) ignore the FIFO and connect to the RCON port directly in their `send-command` script.

### Example: Minecraft Java

```dockerfile
FROM registry.0xkowalski.dev/gamejanitor/base

RUN apt-get update && apt-get install -y openjdk-21-jre-headless && rm -rf /var/lib/apt/lists/*

COPY scripts/ /scripts/
COPY defaults/ /defaults/

EXPOSE 25565
```

- `install-server`: Downloads the Minecraft server jar
- `start-server`: Creates FIFO, runs `tail -f /tmp/cmd-input | java -jar server.jar nogui`
- `stop-server`: Sends `stop` via FIFO, waits for process to exit
- `send-command`: Writes to `/tmp/cmd-input` FIFO
- `update-server`: Downloads the latest jar, replacing the old one

### Example: Rust

- `install-server`: SteamCMD install of Rust Dedicated Server
- `start-server`: `exec ./RustDedicated -batchmode +server.port ${SERVER_PORT} +server.maxplayers ${SERVER_MAXPLAYERS} +rcon.port ${RCON_PORT} +rcon.password ${RCON_PASSWORD} ...` (env vars translated to CLI args)
- `stop-server`: Sends `quit` via RCON, waits for process to exit
- `send-command`: Connects via RCON to `localhost:${RCON_PORT}` with `${RCON_PASSWORD}` and sends the command
- `update-server`: SteamCMD update

---

## CLI

All commands talk to the Gamejanitor HTTP API. The CLI is designed to be agent-friendly.

### Global Flags

| Flag | Default | Description |
|---|---|---|
| `--json` | false | Output as JSON |
| `--api-url` | `http://localhost:8080` | API base URL |

### JSON Output Format

All commands with `--json` use a consistent envelope:

```json
{"status": "ok", "data": { ... }}
{"status": "error", "error": "Port 25565/tcp is already in use by Docker. Check for conflicting services."}
```

### Commands

```
gamejanitor serve [--port 8080] [--data-dir /var/lib/gamejanitor]

gamejanitor games list
gamejanitor games get <id>
gamejanitor games create --id <slug> --name <name> --image <image> [--default-ports '<json>'] [--default-env '<json>']
gamejanitor games update <id> [--name <name>] [--image <image>] ...
gamejanitor games delete <id>

gamejanitor gameservers list [--game <game-id>] [--status <status>]
gamejanitor gameservers get <id>
gamejanitor gameservers create --name <name> --game <game-id> [--port game:25565/tcp] [--env KEY=VALUE] [--memory 4096] [--cpu 2.0]
gamejanitor gameservers update <id> [--name <name>] [--port game:25566/tcp] [--memory 8192] ...
gamejanitor gameservers delete <id>

gamejanitor gameservers start <id>
gamejanitor gameservers stop <id>
gamejanitor gameservers restart <id>
gamejanitor gameservers update-game <id>
gamejanitor gameservers reinstall <id>
gamejanitor gameservers status <id>

gamejanitor gameservers logs <id> [--tail <n>] [--follow]
gamejanitor gameservers send <id> <command>

gamejanitor schedules list --gameserver <id>
gamejanitor schedules create --gameserver <id> --name <name> --type <type> --cron <expr> [--payload '<json>']
gamejanitor schedules update <id> [--enabled true|false] [--cron <expr>]
gamejanitor schedules delete <id>

gamejanitor backups list --gameserver <id>
gamejanitor backups create --gameserver <id> [--name <name>]
gamejanitor backups restore <backup-id>
gamejanitor backups delete <backup-id>
```

### Idempotent Operations

Operations should be safe to retry:
- **Start** on already running/started gameserver → no-op, return current status
- **Stop** on already stopped gameserver → no-op, return current status
- **Restart** on stopped gameserver → just starts it
- **Delete** on running gameserver → stops it first, then deletes

### Port Conflict Handling

Port conflicts are detected at Docker container creation time, not at the application level. When Docker rejects a port binding, Gamejanitor surfaces a clear error message: `"Failed to start gameserver: port 25565/tcp is already in use. Check for conflicting services or other gameservers using this port."`

### Update-Game Flow

`update-game` stops the gameserver, then starts a temporary container from the same image mounting the same volume and runs `/scripts/update-server` inside it. After the update script completes, the temporary container is removed and the gameserver is started normally.

### Reinstall Behavior

`reinstall` stops the gameserver, removes the container, deletes the `.installed` marker in the volume (but preserves all other user data and config files), then starts the gameserver. The `install-server` script runs again on next start. This re-downloads/re-installs the game server binary without wiping user worlds, configs, or mods.

### Game Deletion

Deleting a game is prevented if any gameservers reference it. Returns error: `"Cannot delete game 'minecraft-java': 2 gameservers still reference this game. Delete or reassign them first."`

---

## WebUI

### Pages

| Route | Page | Description |
|---|---|---|
| `/` | Dashboard | Overview of all gameservers with live status, player counts, quick actions |
| `/games` | Games | List/manage game definitions |
| `/games/:id` | Game Detail | View/edit game definition |
| `/gameservers/new` | New Gameserver | Create gameserver form, game selection, port/env configuration |
| `/gameservers/:id` | Gameserver Detail | Gameserver overview, status, config, actions |
| `/gameservers/:id/console` | Console | Live console output (SSE) + command input |
| `/gameservers/:id/files` | File Browser | Directory listing, file editor |
| `/gameservers/:id/files/*path` | File Editor | Edit a specific file (CodeMirror) |
| `/gameservers/:id/schedules` | Schedules | Manage scheduled tasks |
| `/gameservers/:id/backups` | Backups | Manage backups |

### Interactivity Model

- **HTMX**: Page navigation, form submissions, server actions (start/stop/restart), partial page updates, polling for status
- **Alpine.js**: Modals, confirmation dialogs, client-side form validation, file tree expand/collapse, tabs
- **SSE**: Console output streaming, dashboard status updates
- **CodeMirror**: File editing with syntax highlighting

### Conditional UI by Capabilities

The gameserver detail page and its sub-pages check the game's `disabled_capabilities` before rendering sections:

- `console_send` disabled → hide command input on console page
- `console_read` disabled → hide console page entirely
- `query` disabled → hide player list and query data on dashboard/status
- `mod_support` disabled → hide mod management UI
- `update` disabled → hide update button
- `save_command` disabled → skip save step before backups (no warning needed, just doesn't save)

---

## File Browser

Accesses game server files via `docker cp` and `docker exec`. This avoids needing direct host access to Docker volume paths, which may not be accessible depending on permissions or Docker configuration.

### Operations

| Operation | Method |
|---|---|
| List directory | `docker exec <container> ls -la <path>` (running) or start temporary container from same volume (stopped) |
| Read file | `docker cp <container>:<path> -` |
| Write file | `docker cp - <container>:<path>` |
| Delete file | `docker exec <container> rm <path>` |
| Create directory | `docker exec <container> mkdir -p <path>` |
| Upload file | `docker cp` from temp file |

Note: `docker cp` works on stopped containers. For operations requiring `docker exec` on a stopped server, Gamejanitor starts a temporary helper container mounting the same volume.

### Path Validation

All file paths must be sanitized to prevent directory traversal. Resolve paths to absolute, confirm they're rooted within `/data`, and reject anything containing `..` sequences. Return a clear error: `"Invalid path: must be within /data directory."`

### Features

- Directory listing with file size, modification time
- Create/delete files and directories
- Upload files
- Edit text files with CodeMirror (syntax highlighting based on file extension)

---

## Backup & Restore

### Backup Process

1. If gameserver is running and game has `save_command` capability → send save command, wait briefly for flush
2. Create tar archive from the Docker volume via `docker exec` or `docker cp`
3. Record backup in DB

No container pausing — the brief inconsistency risk is acceptable and avoids freezing connected players.

### Restore Process

1. Stop the gameserver if running
2. Clear the volume contents
3. Extract backup tar into the volume
4. Start the gameserver (if it was running before)

### Storage

Backups stored in `<data-dir>/backups/<gameserver-id>/<backup-id>.tar.gz`

---

## Scheduling

Uses an in-process cron scheduler (e.g. `robfig/cron` Go library).

### Task Types

| Type | Behavior |
|---|---|
| `restart` | Stop then start the gameserver |
| `backup` | Run the backup process |
| `command` | Send a command via `send-command` (requires `console_send` capability) |
| `update` | Stop gameserver, run `update-server`, start gameserver |

On Gamejanitor startup, all enabled schedules are loaded and registered with the cron scheduler. Schedule CRUD operations update both the DB and the in-memory scheduler.

---

## Image Pulling

Images are pulled on every `gameserver start`. Docker's layer caching makes this fast when the image hasn't changed — it only downloads new layers. This means users automatically get game server updates when they restart their server. If the pull fails (network issue, registry down), the start continues with the locally cached image if one exists.

---

## NixOS Module

The NixOS module configures Gamejanitor itself (the service), not individual gameservers. Gameservers are managed through the WebUI or CLI.

```nix
# nixos/module.nix
{ config, lib, pkgs, ... }:

let
  cfg = config.services.gamejanitor;
in {
  options.services.gamejanitor = {
    enable = lib.mkEnableOption "Gamejanitor game server manager";

    port = lib.mkOption {
      type = lib.types.port;
      default = 8080;
      description = "Port for the web UI and API";
    };

    dataDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/gamejanitor";
      description = "Directory for database and backups";
    };
  };

  config = lib.mkIf cfg.enable {
    virtualisation.docker.enable = true;

    systemd.services.gamejanitor = {
      description = "Gamejanitor Game Server Manager";
      after = [ "network.target" "docker.service" ];
      wants = [ "docker.service" ];
      wantedBy = [ "multi-user.target" ];

      serviceConfig = {
        ExecStart = "${pkgs.gamejanitor}/bin/gamejanitor serve --port ${toString cfg.port} --data-dir ${cfg.dataDir}";
        Restart = "always";
        RestartSec = 5;
        SupplementaryGroups = [ "docker" ];
        DynamicUser = true;
        StateDirectory = "gamejanitor";
      };
    };
  };
}
```

---

## Nix Flake

```nix
# flake.nix provides:
# - devShell: Go toolchain, Docker CLI, SQLite, nix-related tools
# - packages.gamejanitor: the built Go binary
# - nixosModules.default: the NixOS module
# - apps: convenience scripts
#   - build-image <game>: builds a game Docker image
#   - push-image <game>: pushes to registry.0xkowalski.dev
#   - push-all-images: builds and pushes all game images
```

---

## Implementation Phases

The project structure is a guide, not rigid. Start with fewer files and split when they get large. Merge tightly coupled logic (e.g. GSQ polling into status manager, console into gameserver service) unless file size justifies splitting.

### Phase 1: Foundation
- flake.nix with dev shell, Go toolchain
- Go project scaffold: `go mod init`, directory structure
- Structured logging with `slog`
- SQLite setup with migration runner (WAL mode)
- DB models and seed data (Minecraft Java, Rust as initial games)
- App config loading

### Phase 2: Docker Images — Minecraft Java
- Base image with entrypoint and script stubs
- Minecraft Java image (install, start, stop, send-command, update, health-check)
- Nix scripts for building and pushing images

### Phase 3: Docker Layer
- Docker client wrapper: create/remove containers, create/remove volumes, pull images
- Container lifecycle: start, stop, exec, log streaming
- Docker events watcher goroutine

### Phase 4: Core Service Layer
- Game CRUD service
- Gameserver CRUD service
- Gameserver lifecycle: start/stop/restart flow with status transitions
- Status manager: Docker events → DB updates, GSQ polling
- Crash recovery on startup

### Phase 5: HTTP API
- Router setup with chi
- JSON API endpoints for all CRUD and actions
- SSE endpoint for status updates

### Phase 6: CLI
- Cobra setup with `--json` and `--api-url` flags
- All commands implemented against the HTTP API

### Phase 7: WebUI — Core
- Layout template with Tailwind + HTMX + Alpine
- Dashboard with gameserver list and live status
- Game management pages
- Gameserver CRUD pages
- Gameserver detail page with actions

### Phase 8: Console
- SSE endpoint for log streaming
- Console page with live output
- Command input (checks `console_send` capability)

### Phase 9: File Browser & Editor
- File listing via docker cp/exec
- File read/write endpoints
- File browser page
- CodeMirror editor integration

### Phase 10: Scheduling & Backups
- In-process cron scheduler
- Schedule CRUD
- Backup creation (tar volume via docker)
- Backup restore
- Schedule and backup UI pages

### Phase 11: GSQ Integration
- GSQ poller for running gameservers
- Player list display on dashboard and gameserver detail
- Query data in status endpoint

### Phase 12: More Game Images
- Rust image
- Additional games

### Phase 13: NixOS Module
- Module definition
