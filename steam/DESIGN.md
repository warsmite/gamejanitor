# Steam Depot Downloader

## The Problem

Valve is removing games from anonymous SteamCMD access. Dedicated server files that previously downloaded with `login anonymous` now return "No subscription" errors. Affected games include Project Zomboid, Valheim, Don't Starve Together, and others tracked in [Valve GitHub issue #11459](https://github.com/ValveSoftware/steam-for-linux/issues/11459).

This is a bulk removal from Steam's [Anonymous Dedicated Server Comp](https://steamdb.info/sub/17906/apps/) package. Games where the dedicated server shares the game's app ID (rather than having a separate free server app) now require an authenticated Steam account — and in many cases, one that owns the game.

## Three Auth Tiers

Games declare their requirement in `game.yaml`:

- `steam_login: anonymous` — No account needed (default, most games)
- `steam_login: account` — Any Steam account works, no ownership required
- `steam_login: ownership` — Account must own the game

## Our Solution

A native Go implementation of Steam's CM (Connection Manager) protocol that replaces SteamCMD for authenticated downloads. No passwords stored — uses revocable refresh tokens with ~200 day lifetime.

### How It Works

1. User runs `gamejanitor steam login` once
2. Authenticates via Steam (approve on phone or enter code)
3. Obtains a refresh token stored in the settings DB
4. When a gameserver needs auth-required game files, the worker:
   - Connects to Steam CM via WebSocket
   - Authenticates with the refresh token
   - Resolves the app's depots and manifest via PICS
   - Downloads chunks from Steam's CDN in parallel
   - Decrypts (AES-256) and decompresses (LZMA/zstd) chunks
   - Assembles files on disk in a shared cache
5. Cached files are bind-mounted into the game container at `/depot:ro`
6. The install script copies from `/depot` to `/data/server`
7. Subsequent starts skip the download if the manifest hasn't changed
8. Game updates trigger delta downloads — only changed chunks are fetched

### Architecture

The depot downloader runs on the **worker**, not the controller. Each worker maintains its own local cache at `{data_dir}/cache/depots/{app_id}/merged/`. The controller passes credentials to the worker via gRPC per-request. This means:

- No cross-network file transfer in multi-node setups
- Each worker independently caches and delta-updates
- Multiple gameservers on the same worker share cached game files

### Security

- No Steam passwords stored anywhere
- Refresh tokens are revocable by the user via Steam's device management
- Tokens expire after ~200 days with proactive warnings
- Credentials passed per-request via gRPC, not persisted on workers
