# Game**Janitor**

Host game servers without the headache. One binary, zero config.

> **Status:** Pre-release. Actively developed, used in production on a homelab. API and config may change.

## Why GameJanitor?

- **Single binary** — no runtime dependencies, no databases to install, no PHP, no Redis. Just the binary and Docker.
- **Works immediately** — download, run, open the UI. Sane defaults mean you don't need to configure anything to get started.
- **Built for multi-node** — distribute servers across machines with automatic placement, live migration, and a built-in game traffic proxy. Most panels treat multi-node as an afterthought.
- **Web UI and CLI** — full-featured web panel for day-to-day management, CLI with near-complete feature parity for automation and scripting.

## Quick Start

Requires Linux (x86_64) and Docker.

```sh
curl -fsSL https://raw.githubusercontent.com/warsmite/gamejanitor/master/install.sh | sudo sh
```

Open **http://localhost:8080** — create a game server, click start, you're live.

No database to install, no config files, no reverse proxy. GameJanitor is a single binary that includes the web UI, SFTP server, and everything else.

## Supported Games

| Game | Mods | Steam Login Required |
|------|------|---------------------|
| Minecraft: Java Edition | Modrinth (mods + modpacks) | N/A |
| Minecraft: Bedrock Edition | — | N/A |
| Rust | uMod (Oxide/Carbon) | No |
| Counter-Strike 2 | Steam Workshop | No |
| Garry's Mod | Steam Workshop Collections | No |
| ARK: Survival Evolved | Steam Workshop | No |
| Valheim | — | No |
| Terraria (TShock) | — | N/A |
| 7 Days to Die | — | No |
| Palworld | — | No |
| Project Zomboid | — | Yes (game ownership required) |
| Satisfactory | — | No |

Most Steam games can be downloaded anonymously. A few (like Project Zomboid) require a Steam account that owns the game — see [Steam Login](#steam-login) below. Adding new games is a YAML file + shell scripts — see `games/data/README.md`.

## Features

### Web UI
Full management panel — create servers, configure settings, manage mods, browse files, view console, create backups, set up schedules. Real-time stats and log streaming. Dark theme.

### Console
Live log streaming with syntax highlighting. Timestamps dimmed, log levels colored, player joins/leaves highlighted. Command input with history. Session picker for viewing old logs.

### Mods
Browse and install mods from Modrinth, uMod, and Steam Workshop directly in the UI. Modpack support with version/loader compatibility checking. One-click updates.

### Backups
On-demand and scheduled backups. Local or S3-compatible storage (MinIO, Garage, AWS S3). One-click restore. The database is automatically backed up to the same storage.

### Schedules
Cron-based scheduling for restarts, backups, commands, and game updates. Visual builder in the UI or raw cron expressions.

### File Manager
Browse, edit, upload, and download server files from the web UI. SFTP access for bulk transfers.

### Resource Limits
Set memory and CPU limits per gameserver, enforced by Docker. Storage limits are tracked for placement decisions and warnings but not hard-enforced. Auto-restart on crash is enabled by default.

### Webhooks
HTTP webhooks for gameserver events — server started, stopped, crashed, backup completed, player counts, etc. Point them at Discord, Slack, or any HTTP endpoint.

### CLI
The same binary is both the server and a full CLI client. Near-complete feature parity with the web UI — manage servers, backups, schedules, tokens, settings, and more from the terminal.

```sh
gamejanitor create --game minecraft-java --name "My Server"
gamejanitor start my-server
gamejanitor logs my-server --follow
gamejanitor backups create my-server
```

### Multi-Node
Distribute game servers across multiple machines. Automatic placement based on available resources. Live migration between nodes without changing the connect address.

### Game Traffic Proxy
Optional TCP/UDP proxy on the controller forwards game traffic to worker nodes. Players always connect to the same IP and port — migration is transparent. Designed for homelabs behind a single public IP, but also useful for business deployments that want a single entry point.

### Steam Login

Valve is [removing games from anonymous SteamCMD access](https://github.com/ValveSoftware/steam-for-linux/issues/11459). Dedicated servers that previously worked with `login anonymous` now require an authenticated account — and some require ownership of the game.

GameJanitor handles this with a native Go depot downloader that replaces SteamCMD entirely. Most games still download anonymously, but for those that don't:

```sh
gamejanitor steam login
```

This authenticates via Steam (approve on your phone or enter a code) and stores only a refresh token — your username and password are never saved. The token is long-lived (~200 days), revocable via Steam's device management, and only needs to be refreshed when it expires.

See `steam/DESIGN.md` for the full technical details.

## Installation

### Install Script (Recommended)

```sh
curl -fsSL https://raw.githubusercontent.com/warsmite/gamejanitor/master/install.sh | sudo sh
```

Downloads the latest binary, installs it, and sets up a systemd service that starts on boot.

### CLI Only

If you just want the binary without the service (for manual control, scripting, or running in the foreground):

```sh
curl -Lo gamejanitor https://github.com/warsmite/gamejanitor/releases/latest/download/gamejanitor-linux-amd64
chmod +x gamejanitor
sudo mv gamejanitor /usr/local/bin/

# Run in the foreground (needs Docker access — use sudo or add yourself to the docker group)
sudo gamejanitor serve

# Or install the service later
sudo gamejanitor install
```

### NixOS

Add to your flake inputs:

```nix
inputs.gamejanitor.url = "github:warsmite/gamejanitor";
```

Then in your configuration:

```nix
{ inputs, ... }: {
  imports = [ inputs.gamejanitor.nixosModules.default ];

  services.gamejanitor = {
    enable = true;
    bindAddress = "0.0.0.0";
    openFirewall = true;
  };
}
```

See `nixos/module.nix` for all options including multi-node worker configuration, S3 backup storage, TLS, and resource limits.

## Configuration

GameJanitor works out of the box, for most people, with zero configuration. All settings have sensible defaults.

### Config File

```sh
gamejanitor init  # generates a starter config
gamejanitor serve --config gamejanitor.yaml
```

```yaml
bind: "0.0.0.0"
port: 8080
sftp_port: 2222
data_dir: /var/lib/gamejanitor

# S3 backups (optional — defaults to local storage)
backup_store:
  type: s3
  endpoint: s3.amazonaws.com
  bucket: my-gameserver-backups
  region: us-east-1

# Runtime settings (written to DB on startup)
settings:
  auth_enabled: true
  localhost_bypass: false
  max_backups: 10
```

### CLI Flags

Config options can also be set via flags:

```sh
gamejanitor serve --bind 0.0.0.0 --port 8080 --sftp-port 2222 --data-dir /data
```

### Authentication

Auth is disabled by default. To enable, go to Settings → Security in the web UI, create a token, and flip the auth toggle. Share access with friends using invite links from the Tokens page.

Everything can also be done via CLI:

```sh
gamejanitor tokens offline create --name admin --type admin
gamejanitor settings set auth_enabled true
```

## Multi-Node

Run game servers across multiple machines. The controller manages the cluster; workers run the containers.

### Controller

```sh
gamejanitor serve --bind 0.0.0.0 --grpc-port 9090
```

### Workers

```sh
# On the controller, create a worker token:
gamejanitor tokens offline create --name worker-1 --type worker

# On the worker machine:
gamejanitor serve --worker --controller=false \
  --controller-address controller-host:9090 \
  --worker-token <token>
```

Workers auto-register, receive TLS certificates, and start accepting game servers. The controller handles placement based on available memory, CPU, and storage.

### Game Traffic Proxy

For setups where all nodes share one public IP, enable the proxy on the controller:

```sh
gamejanitor serve --proxy
```

Game traffic is forwarded from the controller to whichever worker hosts each server. Players connect to the controller's IP. 

## Building from Source

### With Nix (recommended)

The Nix flake includes all dev tooling, build scripts, and test commands:

```sh
nix develop          # enter dev shell
dev                  # build UI + run server locally
cli <command>        # run CLI commands via go run
test                 # go test ./...
test-all             # includes integration tests
build                # build UI + Go binary
gen-proto            # regenerate gRPC proto
```

```sh
nix build            # reproducible binary
```

### Without Nix

```sh
# Requires: Go 1.25+, Node.js 20+, protoc

cd ui && npm install && npm run build && cd ..
go build -o gamejanitor .
go test ./...
```

## License

MIT
