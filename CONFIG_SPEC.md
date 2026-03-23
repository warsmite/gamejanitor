# Configuration Specification

## Overview

Configuration has two layers with clear ownership:

1. **Config file** — infrastructure settings. Read once at startup. Optional. Newbies don't need one.
2. **Database** — operational settings. Managed via API/UI at runtime. Source of truth for anything not in the config file.

No env var overrides for settings. Env vars are only for secrets (S3 keys, worker tokens) and containerized deployments where file mounting is awkward.

## Config File

YAML file, optional, specified via `--config` flag or discovered at default locations (`./gamejanitor.yaml`, `/etc/gamejanitor/config.yaml`).

### Infrastructure Settings

These are read at startup and cannot change without a restart. They are NOT stored in the DB.

```yaml
# Network
bind: 0.0.0.0          # default: 127.0.0.1
port: 8080              # default: 8080
grpc_port: 9090         # default: 9090 (0 to disable)
sftp_port: 2222         # default: 2222 (0 to disable)
web_ui: true            # default: true

# Components
controller: true        # default: true
worker: true            # default: true

# Multi-node (worker mode)
controller_address: ""  # controller gRPC address (e.g. 10.0.0.1:9090)
worker_id: ""           # default: hostname
worker_token: ""        # or GJ_WORKER_TOKEN env var

# Storage
data_dir: /var/lib/gamejanitor   # default: /var/lib/gamejanitor

# S3 backup store (optional, falls back to local storage)
s3:
  endpoint: ""
  bucket: ""
  region: ""
  access_key: ""          # or GJ_S3_ACCESS_KEY env var
  secret_key: ""          # or GJ_S3_SECRET_KEY env var
  path_style: false
  use_ssl: true
```

### Initial Settings

These are applied to the DB on **first boot only** (when the settings table is empty). After that, the DB owns them and the config file's `settings` block is ignored.

This lets businesses deploy a config file that bootstraps the system, then manage settings via API without the config file overriding their changes.

```yaml
settings:
  auth_enabled: true
  localhost_bypass: false
  max_backups: 5
  require_memory_limit: true
  require_cpu_limit: true
  require_storage_limit: true
  rate_limit_enabled: true
  rate_limit_per_ip: 20
  rate_limit_per_token: 10
  rate_limit_login: 10
  trust_proxy_headers: false
  event_retention_days: 30
  port_range_start: 27000
  port_range_end: 28999
  port_mode: auto
  connection_address: ""
```

## Environment Variables

Env vars are limited to secrets and container-specific overrides. They do NOT override DB settings.

| Variable | Purpose |
|---|---|
| `GJ_S3_SECRET_KEY` | S3 secret key (avoid putting in config file) |
| `GJ_S3_ACCESS_KEY` | S3 access key (alternative to config file) |
| `GJ_WORKER_TOKEN` | Worker auth token (alternative to config file) |

That's it. No `GJ_AUTH_ENABLED`, no `GJ_MAX_BACKUPS`, no `GJ_PORT_RANGE_START`. Operational settings are in the DB, managed via API.

## Database Settings

All operational settings live in the `settings` key-value table. Managed exclusively via:
- `GET /api/settings` — read all settings
- `PATCH /api/settings` — update settings
- Web UI settings page

Settings are never "locked by environment variable." Every setting is always editable via the API. The config file's `settings` block only seeds initial values on first boot.

### Available Settings

| Key | Type | Default | Description |
|---|---|---|---|
| `connection_address` | string | "" (auto-detect) | Public IP for gameserver connections |
| `port_range_start` | int | 27000 | Auto-allocated port range start |
| `port_range_end` | int | 28999 | Auto-allocated port range end |
| `port_mode` | string | "auto" | Default port allocation mode |
| `max_backups` | int | 10 | Global backup retention limit |
| `auth_enabled` | bool | false | Enable token-based authentication |
| `localhost_bypass` | bool | true | Skip auth for localhost requests |
| `rate_limit_enabled` | bool | false | Enable API rate limiting |
| `rate_limit_per_ip` | int | 20 | Requests per second per IP |
| `rate_limit_per_token` | int | 10 | Requests per second per token |
| `rate_limit_login` | int | 10 | Login attempts per minute |
| `trust_proxy_headers` | bool | false | Trust X-Forwarded-For headers |
| `event_retention_days` | int | 30 | Days to keep event history |
| `require_memory_limit` | bool | false | Require memory_limit_mb > 0 on create/update |
| `require_cpu_limit` | bool | false | Require cpu_limit > 0 on create/update |
| `require_storage_limit` | bool | false | Require storage_limit_mb > 0 on create/update |

## Startup Validation

On startup, the server validates the configuration makes sense:

| Condition | Behavior |
|---|---|
| Auth enabled + no tokens in DB | Hard error: "create a token first: `gamejanitor token create --type admin`" |
| S3 configured but unreachable | Hard error: "cannot connect to S3 at {endpoint}" |
| Docker not accessible | Hard error: "cannot connect to Docker — add user to docker group or run as root" |
| Controller mode + no gRPC port | Warning: "controller mode but gRPC disabled — remote workers cannot connect" |
| Worker mode + no controller address | Hard error: "worker mode requires --controller-address" |

## Migration from Current System

### What changes

1. **Remove all `GJ_*` env var overrides** from `SettingsService` (except secrets)
2. **Remove `FromEnv` methods** — no more `IsAuthEnabledFromEnv()`, etc.
3. **Remove `from_env` fields** from settings API response
4. **Remove "locked by env" UI state** — every setting is always editable
5. **Add config file parser** — YAML, read once at startup
6. **Add first-boot seeding** — apply `settings` block to empty DB
7. **Add startup validation** — check auth+tokens, S3 connectivity, Docker access
8. **Simplify SettingsService** — just DB → default, no env/config override chain

### What stays the same

- Settings API endpoints (`GET /PATCH /api/settings`)
- Settings key-value DB table
- Web UI settings page (simplified — no "locked by env" indicators)
- All setting keys and their behavior

## Per-Archetype Flow

### Newbie

```bash
gamejanitor serve
# Opens browser, changes settings in UI
# Everything stored in DB, no config file
```

### Power User

```bash
gamejanitor init
# Generates gamejanitor.yaml with comments
# Edit: add S3 config, change bind address
gamejanitor serve --config gamejanitor.yaml
# Changes operational settings in UI or API
```

### Business

```bash
# Ansible deploys config file to /etc/gamejanitor/config.yaml
# Config has: bind, S3, initial settings (auth, require limits, rate limits)

# Create admin token offline
gamejanitor token create --type admin --data-dir /var/lib/gamejanitor
# → gj_abc123... (stored in Ansible vault)

# Start server
systemctl start gamejanitor

# Automation configures via API
curl -X POST http://controller:8080/api/webhooks ...
curl -X POST http://controller:8080/api/tokens ...
```
