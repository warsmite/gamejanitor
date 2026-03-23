# Game Definitions

Gamejanitor uses a hybrid approach for game definitions: games ship embedded in the binary and can be overridden or extended by placing game directories in `{dataDir}/games/`.

## Structure

Each game is a directory containing:

```
minecraft-java/
  game.yaml       # Game metadata, ports, environment variables
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

## Resolution Order

1. **`{dataDir}/games/{id}/`** — User overrides (highest priority)
2. **Embedded games** — Shipped with the binary (fallback)

User overrides completely replace the embedded game definition for that ID. To customize a single script, copy the full game directory and modify what you need.

## game.yaml Format

```yaml
id: minecraft-java
name: "Minecraft: Java Edition"
base_image: ghcr.io/warsmite/gamejanitor/java    # Docker image to use
recommended_memory_mb: 2048
gjq_slug: minecraft-java                  # GJQ query slug (optional, defaults to id)
disabled_capabilities: []                 # e.g. ["query", "command", "save"]

ports:
  - name: game
    port: 25565
    protocol: tcp

env:
  - key: EULA
    default: "false"
    label: "Accept Minecraft EULA"
    type: boolean
    required: true
    notice: "HTML notice shown in the UI"
  - key: SERVER_PORT
    default: "25565"
    system: true      # Hidden from users, managed internally
```

### Env Var Types

| Type | Description |
|------|-------------|
| *(empty)* | Free-text input |
| `boolean` | Toggle (true/false) |
| `number` | Numeric input |
| `select` | Dropdown, requires `options` list |
| `version-select` | Special Minecraft version picker |

### Special Env Fields

| Field | Description |
|-------|-------------|
| `system: true` | Hidden from users, set automatically |
| `autogenerate: password` | Auto-generates a random value on gameserver creation |
| `required: true` | Must be set before the server can start |

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
2. Add a `game.yaml` with the required fields
3. Add scripts (`install-server` and `start-server` are required)
4. Restart Gamejanitor — the game appears in the UI
