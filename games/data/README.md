# Game Definitions

Game definitions are the single source of truth for game data across all gamejanitor projects (gamejanitor, gjq, gamejanitorbrowser, gamejanitorhosting). Games ship embedded in the binary and can be overridden or extended by placing game directories in `{dataDir}/games/`.

## Types of Games

- **Container games** — Have a `container:` section with Docker image, env vars, etc. These are games gamejanitor can host.
- **Query-only games** — Have a `query:` section but no `container:`. Used by gjq for server querying.

## Structure

Container games are a directory containing:

```
minecraft-java/
  game.yaml       # Game metadata, ports, query config, container config
  scripts/        # Shell scripts that run inside the container
    install-server
    start-server
    send-command
    save-server
    update-server  (optional)
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
  - name: query                        # Explicit query port (optional, defaults to game port)
    port: 25565
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
    use_external_auth: true
    attributes:
      name: NAME_s

# Container config — used by gamejanitor for hosting (omit for query-only games)
container:
  image: ghcr.io/warsmite/gamejanitor/java
  recommended_memory_mb: 2048
  ready_pattern: 'Done \(\d+\.\d+s\)!'
  disabled_capabilities: []            # e.g. ["command", "save"]
  env:
    - key: EULA
      default: "false"
      label: "Accept Minecraft EULA"
      type: boolean
      consent_required: true
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
| `system: true` | Hidden from users, set automatically |
| `autogenerate: password` | Auto-generates a random value on gameserver creation |
| `required: true` | Must be set before the server can start |
| `triggers_install: true` | Changing this value triggers a full reinstall |
| `consent_required: true` | Requires explicit user consent (e.g. EULA) |

## Script Interface

Scripts run inside a Docker container with the game's base image. They are bind-mounted at `/scripts/` (read-only). Game data lives on a Docker volume at `/data/`.

| Script | When it runs | Purpose |
|--------|-------------|---------|
| `install-server` | First start (when `/data/.installed` doesn't exist) | Download and install the game server |
| `start-server` | Every start | Launch the game server process |
| `send-command` | User sends a console command | Execute a command (e.g. via RCON) |
| `save-server` | Before backups, before graceful stop | Trigger a world save |
| `update-server` | User clicks "Update" | Update the game server to latest version |

## Base Images

<!-- TODO: migrate base images to ghcr.io/gamejanitor when going public -->

| Image | Contents | Used by |
|-------|----------|---------|
| `ghcr.io/warsmite/gamejanitor/base` | Ubuntu 24.04, curl, wget, entrypoint | Minecraft Bedrock |
| `ghcr.io/warsmite/gamejanitor/steamcmd` | base + SteamCMD, rcon-cli, 32-bit libs | Most games (Rust, CS2, ARK, etc.) |
| `ghcr.io/warsmite/gamejanitor/java` | base + JDK 21 | Minecraft Java |
| `ghcr.io/warsmite/gamejanitor/dotnet` | base + .NET 9 | Terraria |

## Adding a Custom Game

1. Create a directory in `{dataDir}/games/` with your game ID
2. Add a `game.yaml` with at least `id`, `name`, `ports`, and a `container:` section
3. Add scripts (`install-server` and `start-server` are required)
4. Restart Gamejanitor — the game appears in the UI
