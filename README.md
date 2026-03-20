# Gamejanitor

User-first game server management. Single binary, web UI + CLI + REST API. Supports single-node (personal use) and multi-node (business/hosting) deployments through the same codebase.

## Vision

Make self-hosted game servers accessible to normal people while still supporting power users and game server hosting businesses.

- **Single-node**: download, run, manage your servers from a web UI. No Docker knowledge, no YAML files, no command line required.
- **Multi-node**: controller + worker architecture for hosting providers. Token-based auth, S3 backups, SFTP access, per-gameserver permissions. Integrates with existing billing/customer systems via API.

Pterodactyl is business-first and painful to install. LinuxGSM is CLI-only. Gamejanitor is a single binary that just works — and scales when you need it to.

## Ecosystem

- **Gamejanitor** (this repo) — core server manager, always open source
- **GJQ** — game janitor query library + CLI ([github.com/0xkowalskidev/gjq](https://github.com/0xkowalskidev/gjq))
- **Server browser** — Battlemetrics-style public server browser (future, separate repo)

## Architecture

```
Browser ──HTTP──> Page handlers ──> Services ──> Worker ──> Docker
                                       ^
CLI ──HTTP──> API handlers (JSON) ─────┘
                                       ^
SFTP client ──SSH──> SFTP server ──────┘
```

**Worker** abstracts all container and host operations. In standalone mode, `LocalWorker` talks to Docker directly. Multi-node adds `RemoteWorker` (gRPC to worker agents on other machines). Standalone = controller + worker in one process.

**Services** are the core — gameserver lifecycle, backups, schedules, file ops, console, query polling, ready detection, auth. Handlers are thin layers over services.

**Game definitions** are YAML + shell scripts embedded in the binary, overridable locally at `{dataDir}/games/{id}/`. Four shared base images (base, steamcmd, java, dotnet) with game scripts bind-mounted at runtime.

### Key design decisions

- **Log-based ready detection**: each game defines a `ready_pattern` regex in its game.yaml. ReadyWatcher follows container logs and promotes Started → Running on match. More reliable than query-based detection, works for all games.
- **Direct volume access with fallback**: file operations use direct filesystem access to Docker volumes when running as root/systemd (fast). Falls back to lazy sidecar containers when running inside Docker (works everywhere).
- **Token-based auth, not user-based**: gamejanitor doesn't manage user accounts. Scoped tokens grant permissions on specific gameservers. Hosting businesses integrate with their own customer systems via API tokens.
- **No auth by default**: single-node users bind to localhost, no auth needed. Auth is opt-in when you open it up.

## Auth model

- **No auth** (default): localhost-only, everything accessible
- **Admin token**: generated on first run, full access
- **Scoped tokens**: created by admin, grant specific permissions (start/stop/console/files/backups) on specific gameservers
- **SFTP**: same tokens — username is gameserver ID, password is the token

Hosting businesses don't need gamejanitor to manage users. They create scoped tokens via API and associate them with customers in their own billing system.

## Tech stack

- **Backend:** Go, SQLite, Docker
- **Frontend:** HTMX, Alpine.js, Tailwind CSS
- **CLI:** Cobra (HTTP client to API)
- **Query:** GJQ for live player counts and server info
- **SFTP:** Embedded Go SSH/SFTP server
- **Packaging:** Nix flake + NixOS module, binary install via script

## Install

Requires Docker. One-liner:

```sh
curl -sSL https://raw.githubusercontent.com/0xkowalskidev/gamejanitor/master/install.sh | bash
```

Then run:

```sh
gamejanitor serve
```

Web UI at http://localhost:8080.

## Supported games

Minecraft Java, Minecraft Bedrock, Rust, CS2, ARK, Valheim, 7 Days to Die, Palworld, Satisfactory, Terraria, Garry's Mod. Goal: support all games with community server support.

## Development

```sh
nix develop
dev              # hot-reloading dev server on :8080
cli <command>    # CLI access
push-all-images  # build/push base Docker images
cleanup          # remove all containers, volumes, dev data
```

## Project structure

```
cmd/gamejanitor/         # entrypoint
cmd/cli/                 # CLI commands (HTTP client)
internal/
  worker/                # Worker interface + LocalWorker (Docker + direct volume access)
  games/                 # game store, embedded YAML definitions + scripts + assets
  service/               # business logic (gameserver, backup, schedule, console, query, files, auth)
  models/                # database models
  db/                    # migrations
  web/
    handlers/            # API handlers (JSON) + page handlers (HTML)
    templates/           # Go HTML templates
    static/              # CSS, JS
  sftp/                  # embedded SFTP server
images/                  # base Docker image Dockerfiles (base, steamcmd, java, dotnet)
nixos/                   # NixOS module
```

## Roadmap

- [x] Core gameserver management (create, start, stop, delete, restart, update, reinstall)
- [x] Web UI with real-time status updates (SSE)
- [x] File browser (web + SFTP)
- [x] Console with live log streaming and command execution
- [x] Scheduled tasks (backups, restarts, commands, updates)
- [x] Direct volume file access with sidecar fallback
- [x] Log-based ready detection per game
- [x] Backup storage (local + S3-compatible)
- [x] Token-based auth with scoped permissions
- [x] Embedded SFTP server with per-gameserver credentials
- [x] Multi-node deployment (controller + workers over gRPC with mTLS)
- [x] Per-gameserver resource caps (memory, CPU, storage, backups)
- [x] Per-node resource limits and port ranges
- [x] Rate limiting (per-IP, per-token, per-login)
- [x] Audit logging with configurable retention
- [x] Bulk operations (start/stop/restart all or by node)
- [x] Gameserver migration between nodes
- [x] NixOS module with secret management
- [ ] Server browser (separate repo)
- [ ] Modding/plugin support
- [ ] More games
