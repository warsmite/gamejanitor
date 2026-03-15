# Gamejanitor

User-first game server management. Single binary, web UI + CLI + REST API. Supports single-node (personal use) and multi-node (business) deployments through the same codebase.

## Vision

Make self-hosted game servers accessible to normal people while still supporting power users and businesses. Pterodactyl is business-first and painful to install. LinuxGSM is CLI-only. Gamejanitor is a single binary that just works.

## Ecosystem

- **Gamejanitor** (this repo) — core server manager, always open source
- **GSQ** — game server query library + CLI ([github.com/0xkowalskidev/gsq](https://github.com/0xkowalskidev/gsq))
- **Server browser** — Battlemetrics-style public server browser (future, separate repo)

## Architecture

```
Browser ──HTTP──> Page handlers ──> Services ──> Worker ──> Docker
                                       ^
CLI ──HTTP──> API handlers (JSON) ─────┘
```

**Worker** abstracts all container and host operations. In standalone mode, `LocalWorker` talks to Docker directly. Multi-node adds `RemoteWorker` (gRPC to worker agents on other machines). Standalone = controller + worker in one process.

**Services** are the core — gameserver lifecycle, backups, schedules, file ops, console, query polling. Handlers are thin layers over services.

**Game definitions** are YAML + shell scripts embedded in the binary, overridable locally at `{dataDir}/games/{id}/`. Four shared base images (base, steamcmd, java, dotnet) with game scripts bind-mounted at runtime.

## Tech stack

- **Backend:** Go, SQLite, Docker
- **Frontend:** HTMX, Alpine.js, Tailwind CSS
- **CLI:** Cobra (HTTP client to API)
- **Query:** GSQ for live player counts and server info
- **Packaging:** Nix flake + NixOS module (Docker Compose and binary install planned)

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
  service/               # business logic (gameserver, backup, schedule, console, query, files)
  models/                # database models
  db/                    # migrations
  web/
    handlers/            # API handlers (JSON) + page handlers (HTML)
    templates/           # Go HTML templates
    static/              # CSS, JS
images/                  # base Docker image Dockerfiles (base, steamcmd, java, dotnet)
nixos/                   # NixOS module
```
