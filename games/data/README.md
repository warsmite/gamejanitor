# Game Definitions

Game definitions are the single source of truth for game data across all gamejanitor projects (gamejanitor, gjq, gamejanitorbrowser, gamejanitorhosting). Games ship embedded in the binary and can be overridden or extended by placing game directories in `{dataDir}/games/`.

## Types of Games

- **Instance games** — Have an `instance:` section with env vars, scripts, etc. These are games gamejanitor can host.
- **Query-only games** — Have a `query:` section but no `instance:`. Used by gjq for server querying.

## Structure

Instance games are a directory containing:

```
minecraft-java/
  game.yaml       # Game metadata, ports, query config, instance config
  scripts/        # Shell scripts that run inside the container
    install-server
    start-server
    stop-server
    send-command
    save-server
    update-server
  assets/         # UI images
    icon.ico
  defaults/       # Default config files copied on first run (optional)
    server.properties
```

Query-only games are a directory with just `game.yaml`.

## Resolution Order

1. **`{dataDir}/games/{id}/`** — User overrides (highest priority)
2. **Embedded games** — Shipped with the binary (fallback)

User overrides completely replace the embedded game definition for that ID. To customize a single script, copy the full game directory and modify what you need.

## game.yaml Format

```yaml
# Shared identity — used by all consumers
id: minecraft-java
name: "Minecraft: Java Edition"
aliases: [minecraft, mc]
app_id: 0                              # Steam AppID (optional, 0 for non-Steam)

ports:
  - name: game
    port: 25565
    protocol: tcp
  - name: rcon
    port: 25575
    protocol: tcp

# Query config — used by gjq for server status queries
query:
  protocol: minecraft                  # source, minecraft, raknet, eos, quake3, fivem, tshock
  supports: [players, mods]
  notes: "Player list limited to 12"   # Limitations shown in gjq output
  eos:                                 # Only for EOS protocol games
    client_id: "..."
    client_secret: "..."
    deployment_id: "..."

# Instance config — used by gamejanitor for hosting (omit for query-only games)
instance:
  # image: omitted — defaults to ghcr.io/warsmite/gamejanitor/runtime
  # which bundles Ubuntu + Steam runtime + all supported JDKs + .NET.
  # Only set this for custom games that need a specialized rootfs.
  recommended_memory_mb: 2048
  ready_pattern: 'Done \(\d+\.\d+s\)!'
  disabled_capabilities: []            # e.g. ["command", "save"]
  env:
    - key: EULA
      default: "false"
      label: "Accept Minecraft EULA"
      type: boolean
      group: server
      consent_required: true
      notice: "You must agree to the [Minecraft EULA](https://aka.ms/MinecraftEULA)"
    - key: RCON_PASSWORD
      default: ""
      autogenerate: password
      group: server
    - key: SERVER_PORT
      default: "25565"
      system: true
  mods:
    loader:
      env_key: MODLOADER
      label: "Mod Loader"
      type: select
      options: ["vanilla", "paper", "forge", "fabric"]
      default: "vanilla"
    sources:
      - type: modrinth
        loader_env: MODLOADER
        version_env: MINECRAFT_VERSION

assets:
  icon: minecraft-icon.ico
```

### Env Var Types

| Type | Description |
|------|-------------|
| *(empty)* | Free-text input |
| `boolean` | Toggle (true/false) |
| `number` | Numeric input |
| `select` | Dropdown, requires `options` list or `dynamic_options` |

### Special Env Fields

| Field | Description |
|-------|-------------|
| `system: true` | Internal, hidden from users. Used for ports, timeouts, etc. |
| `hidden: true` | Exists but not shown in UI. Used for internal toggles (e.g. MODLOADER, OXIDE_ENABLED) |
| `autogenerate: password` | Auto-generates a random value on gameserver creation if not set |
| `required: true` | Must be set before the server can start. Unused by built-in games — all have defaults. For custom games needing user-provided values (e.g. license keys). |
| `consent_required: true` | Requires explicit user consent (e.g. EULA acceptance) |
| `triggers_install: true` | Changing this value triggers a full reinstall |
| `group: <name>` | Groups env vars in the UI (e.g. "server", "gameplay", "world", "performance") |
| `notice: "<markdown>"` | Help text shown below the field in the UI |
| `dynamic_options` | Options loaded at runtime from an external source (e.g. Mojang version API) |

## Script Interface

Scripts run inside the game's OCI image. They are bind-mounted at `/scripts/` (read-only). Game data lives on a volume at `/data/`.

| Script | When it runs | Purpose |
|--------|-------------|---------|
| `install-server` | First start (separate short-lived instance) | Download and install the game server |
| `start-server` | Every start (long-lived instance entrypoint) | Configure and launch the game server process |
| `stop-server` | Before container stop | Announce shutdown, save world, prepare for SIGTERM |
| `send-command` | User sends a console command | Execute a command via RCON or stdin pipe |
| `save-server` | Before backups | Trigger a world save |
| `update-server` | User clicks "Update" | Update the game server to latest version |

### Script Conventions

Every script must follow these conventions:

**Structure:**
```bash
#!/bin/bash
set -e  # Required for install-server and start-server. Optional for stop/save/send.
```

**Logging — always use `[script-name]` prefix:**
```bash
# install-server
echo "[install-server] installing <game name> from depot cache"
echo "[install-server] <game name> installed"

# start-server — log config summary, then "starting"
echo "[start-server] config: <key=val pairs of important user-facing settings>"
echo "[start-server] starting <game name>"

# stop-server
echo "[stop-server] announcing shutdown"
echo "[stop-server] save complete, ready for shutdown"

# save-server
echo "[save-server] saving world"
echo "[save-server] save complete"

# Errors
echo "[start-server] ERROR: <what went wrong>"
```

**Never log passwords or secrets.**

**start-server must:**
1. Write/update config files from env vars
2. Log a config summary with key settings
3. Log "starting <game name>"
4. Use `exec run-gameserver <binary> [args...]` to launch the game.
   `run-gameserver` is a wrapper shipped in the runtime image that cleans up
   background helpers (e.g. `tail -f` on the command FIFO, backgrounded `tee`)
   when the game process exits. Always use it — not using it works by accident
   when there are no background helpers, but fails silently when a future script
   change adds one. One rule: always wrap.

**stop-server must:**
1. Announce shutdown to players via RCON/chat (if available)
2. Trigger a world save (if available)
3. Exit — the runtime sends SIGTERM after this script completes

**install-server must:**
1. Download the game server binary/files
2. Log progress for long operations
3. Exit 0 on success — the controller marks the gameserver as installed on successful exit

**send-command patterns:**
- RCON games: `rcon -a "localhost:${RCON_PORT}" -p "$RCON_PASSWORD" "$1"`
- FIFO games: `echo "$1" > /tmp/cmd-input`
- No support: `echo "[send-command] <game> does not support remote commands"; exit 1`

**Env var defaults:**
- Use `${VAR:-default}` for optional values with sensible defaults
- Don't provide fallback defaults for autogenerated values (passwords) — they will always be set
- Port defaults should match the game.yaml port definitions

## Runtime Image

All built-in games share a single OCI image: `ghcr.io/warsmite/gamejanitor/runtime`
(built from `oci/Dockerfile` at the repo root).

**What's in it:**
- Ubuntu 24.04 base
- Steam client runtime (32-bit + 64-bit `steamclient.so`) — required by most multiplayer games
- `rcon-cli` — used by any game with RCON support
- `run-gameserver` — process-group-cleanup wrapper (see script conventions below)
- OpenJDK 8, 17, 21, 25 at `/opt/java/{8,17,21,25}/bin/java` — JDK-version-sensitive games pick at runtime
- .NET 9 runtime — Terraria/TShock
- i386 runtime libs (`lib32gcc-s1`, `lib32stdc++6`) — GMod and other 32-bit Source engine games

**Why one fat image instead of per-game slim images:**
- OCI layer dedup + overlayfs mean shared layers are stored once on disk; marginal
  per-gameserver cost is small deltas in the upperdir, not duplicated base layers.
- One Dockerfile to maintain instead of six. One CI job instead of six.
- Game contributors don't need to pick the right base or update an image matrix —
  every game just works out of the box.
- Power users with custom images can still override `instance.image:` per game def.

JDK-version-sensitive games (only Minecraft at present) resolve the required JDK
at install time from authoritative metadata (e.g. Mojang's per-version manifest
exposes `javaVersion.majorVersion`) and persist it to `/data/.java-version`.
`start-server` reads that file and launches `/opt/java/$VER/bin/java`.

## Adding a Custom Game

1. Create a directory in `{dataDir}/games/` with your game ID
2. Add a `game.yaml` with at least `id`, `name`, `ports`, and an `instance:` section
3. Add scripts (`install-server` and `start-server` are required, `stop-server` recommended)
4. Restart Gamejanitor — the game appears in the UI

Use `gamejanitor games get <id>` to verify your game definition loads correctly.

If your game needs tools not in the default runtime image, set `instance.image:`
to a custom OCI image (any registry). Otherwise leave it unset to inherit the
default runtime.
