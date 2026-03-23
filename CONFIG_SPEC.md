# Configuration Specification

## Overview

Configuration has two categories:

1. **Infrastructure settings** — network, storage, components, TLS. Config file and CLI flags only. Read at startup, cannot change without a restart. Not stored in DB.
2. **Runtime settings** — operational behavior (auth, limits, port ranges, etc.). Stored in DB, managed via API/UI at runtime. Can also be set via config file or CLI flags — on every startup, explicitly specified values are written to DB, overwriting whatever was there. Unspecified settings are left alone.

Env vars are only for secrets — backup store credentials and worker tokens. Nothing else.

### Precedence

For runtime settings, the startup precedence is:

```
hardcoded default → config file → CLI flag → written to DB
```

At runtime, the API/UI writes directly to DB. On next restart, config/flag values overwrite their settings again. Settings not in the config file or flags are untouched — DB values persist across restarts.

This means:
- **Newbie** (no config, no flags): defaults populate DB, UI changes persist forever.
- **Power user** (config for some, UI for others): config-specified values reset on restart, UI-only values persist.
- **Business** (everything in config): config is source of truth, API changes are temporary runtime overrides that reset on restart — exactly what declarative deployments want.

There is no "locked" state. Every setting is always editable via the API. The config file just re-applies its values on boot.

## Config File

YAML file, optional, specified via `--config` flag or discovered at default locations (`./gamejanitor.yaml`, `/etc/gamejanitor/config.yaml`).

### Infrastructure Settings

Read at startup only. Not stored in DB. All have sane defaults so newbies don't need a config file.

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

# Storage
data_dir: /var/lib/gamejanitor   # default: /var/lib/gamejanitor

# Multi-node (worker mode)
controller_address: ""  # controller gRPC address (e.g. 10.0.0.1:9090)
worker_id: ""           # default: hostname
worker_token: ""        # or GJ_WORKER_TOKEN env var

# Worker capacity (reported to controller on registration)
# Required in multi-node — the operator declares what each node has.
# The dispatcher uses these to make placement decisions.
worker_limits:
  max_memory_mb: 32768    # total memory available for gameservers
  max_cpu: 16             # total CPU cores available for gameservers
  max_storage_mb: 500000  # total storage available for gameservers
  port_range_start: 27000 # port range this worker owns
  port_range_end: 27999   # port range this worker owns

# TLS for gRPC (controller ↔ worker communication)
# If tls.ca is set, mTLS is enabled. Certs are generated with `gamejanitor gen-worker-cert`.
# Default paths: {data_dir}/certs/ca.pem, {data_dir}/certs/{cert,key}.pem
tls:
  ca: ""                # path to CA certificate
  cert: ""              # path to server/client certificate
  key: ""               # path to private key

# Backup storage (default: local, stored in {data_dir}/backups)
# Supports any S3-compatible backend: AWS S3, MinIO, Backblaze B2, Wasabi, etc.
backup_store:
  type: local             # "local" or "s3" (default: local)
  endpoint: ""            # S3 endpoint (e.g. s3.us-east-1.amazonaws.com, minio.local:9000)
  bucket: ""              # S3 bucket name
  region: ""              # S3 region
  access_key: ""          # or GJ_BACKUP_STORE_ACCESS_KEY env var
  secret_key: ""          # or GJ_BACKUP_STORE_SECRET_KEY env var
  path_style: false       # use path-style URLs (required for MinIO)
  use_ssl: true           # use HTTPS for S3 connections
```

### Runtime Settings

Stored in DB. On every startup, settings explicitly listed here are written to DB. Omitted settings are left at their current DB value (or hardcoded default if no DB value exists).

At runtime, all settings are editable via API/UI regardless of whether they're in the config file. Config-specified values re-apply on next restart.

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

Env vars are only for secrets. Everything else goes in the config file. Containerized deployments mount a config file — Docker bind mounts, K8s ConfigMaps.

| Variable | Purpose |
|---|---|
| `GJ_BACKUP_STORE_SECRET_KEY` | Backup store secret key |
| `GJ_BACKUP_STORE_ACCESS_KEY` | Backup store access key |
| `GJ_WORKER_TOKEN` | Worker auth token (alternative to config file) |

That's it.

## Database Settings

All runtime settings live in the `settings` key-value table. Managed via:
- Config file `settings` block (applied on every startup)
- CLI flags (applied on every startup, override config file)
- `GET /api/settings` / `PATCH /api/settings`
- Web UI settings page

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

On startup, the server validates the configuration:

| Condition | Behavior |
|---|---|
| Auth enabled + no tokens in DB | Hard error: "create a token first: `gamejanitor token create --type admin`" |
| Backup store type "s3" but unreachable | Hard error: "cannot connect to backup store at {endpoint}" |
| Docker not accessible | Hard error: "cannot connect to Docker — add user to docker group or run as root" |
| Controller mode + no gRPC port | Warning: "controller mode but gRPC disabled — remote workers cannot connect" |
| Worker mode + no controller address | Hard error: "worker mode requires controller_address" |
| Worker mode + no worker token | Hard error: "worker mode requires worker_token (or GJ_WORKER_TOKEN)" |
| Worker mode + no worker_limits | Hard error: "worker mode requires worker_limits (max_memory_mb, max_cpu, max_storage_mb, port range)" |
| TLS partially configured (ca without cert/key) | Hard error: "tls.ca is set but tls.cert and tls.key are also required" |
| Controller mode + TLS cert missing | Hard error: "controller mode requires TLS certs — run `gamejanitor gen-worker-cert`" |

---

## Migration from Current System

### What changes

1. **Remove all `GJ_*` env var overrides** from `SettingsService` — no more `GJ_AUTH_ENABLED`, `GJ_MAX_BACKUPS`, etc.
2. **Remove `FromEnv` methods** — no more `IsAuthEnabledFromEnv()`, etc.
3. **Remove `from_env` fields** from settings API response
4. **Remove "locked by env" UI state** — every setting is always editable
5. **Add config file parser** — YAML, read once at startup
6. **Add startup settings apply** — write config/flag-specified settings to DB on every boot
7. **Add startup validation** — check auth+tokens, backup store connectivity, Docker access
8. **Simplify SettingsService** — just DB → default, no env/config override chain
9. **Rename S3 to backup_store** — `s3:` block becomes `backup_store:` with `type: local|s3`
10. **Rename S3 env vars** — `GJ_S3_*` becomes `GJ_BACKUP_STORE_*` (secrets only)
11. **Remove non-secret env vars** — `GJ_S3_BUCKET`, `GJ_S3_ENDPOINT`, `GJ_S3_REGION`, `GJ_S3_PATH_STYLE`, `GJ_S3_USE_SSL`, `GJ_BIND`, `GJ_SFTP_PORT`, `GJ_PORT_RANGE_START`, `GJ_PORT_RANGE_END`, `GJ_MAX_MEMORY`, `GJ_MAX_CPU`, `GJ_MAX_STORAGE`, `GJ_GRPC_CA`, `GJ_GRPC_CERT`, `GJ_GRPC_KEY` — all move to config file
12. **Remove `audit_retention_days`** — replaced by `event_retention_days` (already in spec)

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
# Backups stored locally in {data_dir}/backups
# Settings persist across restarts
```

### Power User

```bash
gamejanitor init
# Generates gamejanitor.yaml with comments
# Edit: add backup store (MinIO NAS), change bind address, set auth_enabled

gamejanitor serve --config gamejanitor.yaml
# Config-specified settings written to DB on startup
# Can still change anything via UI or API at runtime
# On restart, config values re-apply; UI-only changes persist
```

### Business (Single Node)

```bash
# Ansible deploys config file to /etc/gamejanitor/config.yaml
# Config has: bind, backup store, settings (auth, require limits, rate limits)

# Create admin token offline (idempotent — safe in ExecStartPre)
gamejanitor token create --type admin --name default --data-dir /var/lib/gamejanitor
# → gj_abc123... (stored in Ansible vault)

# Start server — settings block written to DB
systemctl start gamejanitor

# API can tweak settings at runtime (e.g. during incident)
# Next restart resets to config values — config is source of truth
```

### Business (Multi-Node)

Fully automatable via Ansible/Terraform. Two-phase deployment: controller first, then workers.

**Phase 1 — Controller node:**

```bash
# 1. Deploy config
cat > /etc/gamejanitor/config.yaml <<EOF
bind: 0.0.0.0
port: 8080
grpc_port: 9090
controller: true
worker: false
data_dir: /var/lib/gamejanitor

backup_store:
  type: s3
  endpoint: s3.us-east-1.amazonaws.com
  bucket: company-gamejanitor-backups
  region: us-east-1

settings:
  auth_enabled: true
  require_memory_limit: true
  require_cpu_limit: true
  rate_limit_enabled: true
EOF

# 2. Create tokens offline (before starting — direct DB access)
gamejanitor token create --type admin --name default --data-dir /var/lib/gamejanitor
# → gj_admin_abc123...
gamejanitor token create --type worker --name worker-pool --data-dir /var/lib/gamejanitor
# → gj_worker_xyz789...

# 3. Generate TLS certs for each worker
gamejanitor gen-worker-cert worker-1 --data-dir /var/lib/gamejanitor
gamejanitor gen-worker-cert worker-2 --data-dir /var/lib/gamejanitor
# Outputs: {data_dir}/certs/worker-1.pem, {data_dir}/certs/worker-1-key.pem, etc.
# CA cert: {data_dir}/certs/ca.pem

# 4. Start controller
systemctl start gamejanitor
```

**Phase 2 — Worker nodes** (after controller is up, certs + token distributed via Ansible):

```bash
# 1. Deploy config
cat > /etc/gamejanitor/config.yaml <<EOF
bind: 0.0.0.0
grpc_port: 9090
sftp_port: 2222
controller: false
worker: true
data_dir: /var/lib/gamejanitor

controller_address: 10.0.0.1:9090
worker_id: worker-1
worker_token: gj_worker_xyz789...

worker_limits:
  max_memory_mb: 32768
  max_cpu: 16
  max_storage_mb: 500000
  port_range_start: 27000
  port_range_end: 27999

tls:
  ca: /etc/gamejanitor/certs/ca.pem
  cert: /etc/gamejanitor/certs/worker-1.pem
  key: /etc/gamejanitor/certs/worker-1-key.pem
EOF

# 2. Start worker — self-registers with controller via gRPC
systemctl start gamejanitor
```

Workers self-register on startup: connect to controller with token, report resource limits and port range, controller dials back to verify connectivity, worker appears in the cluster. Heartbeats every 10s; controller reaps workers after 30s of silence.

**One worker token or one per worker?** One per worker is recommended for revocation — revoking a compromised node's token doesn't affect the others. Each worker gets its own config file with its own token. For simplicity, a shared token across all workers also works but is less secure.

**TLS cert distribution:** `gen-worker-cert` generates certs on the controller using its CA. Ansible copies the CA cert + worker-specific cert/key to each worker node. The controller's own cert is auto-generated on first start when `grpc_port > 0` and `controller: true`.

**TLS auto-discovery:** If `tls` is not set in the config file but `{data_dir}/certs/ca.pem` exists, gamejanitor uses it automatically. This means `gen-worker-cert` + deploying to `{data_dir}/certs/` just works without explicit config. The `tls` block is only needed when certs live outside the data directory.

### Containerized (Docker)

Gamejanitor in Docker manages sibling containers via the Docker socket. Config file is a bind mount — no env vars needed for non-secret settings.

```yaml
# docker-compose.yml
services:
  gamejanitor:
    image: gamejanitor/gamejanitor:latest
    ports:
      - "8080:8080"
      - "9090:9090"
      - "2222:2222"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - gamejanitor-data:/var/lib/gamejanitor
      - ./gamejanitor.yaml:/etc/gamejanitor/config.yaml:ro
    environment:
      - GJ_BACKUP_STORE_ACCESS_KEY=AKIA...
      - GJ_BACKUP_STORE_SECRET_KEY=wJal...
```

Secrets via environment variables, everything else in the mounted config file.
