CREATE TABLE gameservers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    game_id TEXT NOT NULL,
    ports JSON NOT NULL DEFAULT '[]',
    env JSON NOT NULL DEFAULT '{}',
    memory_limit_mb INTEGER NOT NULL DEFAULT 0,
    cpu_limit REAL NOT NULL DEFAULT 0,
    cpu_enforced INTEGER NOT NULL DEFAULT 0,
    instance_id TEXT,
    volume_name TEXT NOT NULL,
    port_mode TEXT NOT NULL DEFAULT 'auto',
    node_id TEXT,
    sftp_username TEXT NOT NULL DEFAULT '',
    hashed_sftp_password TEXT NOT NULL DEFAULT '',
    installed BOOLEAN NOT NULL DEFAULT 0,
    backup_limit INTEGER,
    storage_limit_mb INTEGER,
    node_tags TEXT NOT NULL DEFAULT '{}',
    auto_restart BOOLEAN NOT NULL DEFAULT 0,
    connection_address TEXT,
    applied_config JSON,
    desired_state TEXT NOT NULL DEFAULT 'stopped',
    error_reason TEXT NOT NULL DEFAULT '',
    created_by_token_id TEXT,
    grants JSON NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_gameservers_created_by_token ON gameservers(created_by_token_id) WHERE created_by_token_id IS NOT NULL;

CREATE TABLE schedules (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT NOT NULL REFERENCES gameservers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    payload JSON NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT 1,
    one_shot BOOLEAN NOT NULL DEFAULT 0,
    last_run DATETIME,
    next_run DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE backups (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT NOT NULL REFERENCES gameservers(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'in_progress',
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    hashed_token TEXT NOT NULL,
    token_prefix TEXT NOT NULL DEFAULT '',
    role TEXT NOT NULL DEFAULT 'user',
    can_create BOOLEAN NOT NULL DEFAULT 0,
    max_gameservers INTEGER,
    max_memory_mb INTEGER,
    max_cpu REAL,
    max_storage_mb INTEGER,
    claim_code TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    expires_at DATETIME
);

CREATE INDEX IF NOT EXISTS idx_tokens_prefix ON tokens(token_prefix) WHERE token_prefix != '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_claim_code ON tokens(claim_code) WHERE claim_code IS NOT NULL;

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE worker_nodes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    grpc_address TEXT NOT NULL DEFAULT '',
    lan_ip TEXT NOT NULL DEFAULT '',
    external_ip TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'offline',
    max_memory_mb INTEGER,
    max_cpu REAL,
    max_storage_mb INTEGER,
    cordoned BOOLEAN NOT NULL DEFAULT 0,
    tags TEXT NOT NULL DEFAULT '{}',
    port_range_start INTEGER,
    port_range_end INTEGER,
    sftp_port INTEGER NOT NULL DEFAULT 0,
    last_seen DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE events (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT REFERENCES gameservers(id) ON DELETE SET NULL,
    worker_id TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL,
    actor TEXT NOT NULL DEFAULT '{}',
    data TEXT NOT NULL DEFAULT '{}',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_events_gs_time ON events(gameserver_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_type ON events(type, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_worker_id ON events(worker_id);
CREATE INDEX IF NOT EXISTS idx_gameservers_game_id ON gameservers(game_id);
CREATE INDEX IF NOT EXISTS idx_gameservers_node_id ON gameservers(node_id) WHERE node_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_gameservers_sftp_username ON gameservers(sftp_username) WHERE sftp_username != '';
CREATE INDEX IF NOT EXISTS idx_schedules_gameserver_id ON schedules(gameserver_id);
CREATE INDEX IF NOT EXISTS idx_backups_gameserver_id ON backups(gameserver_id);

CREATE TABLE webhook_endpoints (
    id TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL,
    secret TEXT NOT NULL DEFAULT '',
    events TEXT NOT NULL DEFAULT '["*"]',
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE webhook_deliveries (
    id TEXT PRIMARY KEY,
    webhook_endpoint_id TEXT NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload JSON NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_attempt_at DATETIME,
    next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_error TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_state_next ON webhook_deliveries(state, next_attempt_at);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_endpoint ON webhook_deliveries(webhook_endpoint_id);
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_created_at ON webhook_deliveries(created_at);


CREATE TABLE installed_mods (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT NOT NULL REFERENCES gameservers(id) ON DELETE CASCADE,
    source TEXT NOT NULL,
    source_id TEXT NOT NULL,
    category TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    version TEXT NOT NULL DEFAULT '',
    version_id TEXT NOT NULL DEFAULT '',
    file_path TEXT NOT NULL DEFAULT '',
    file_name TEXT NOT NULL DEFAULT '',
    download_url TEXT NOT NULL DEFAULT '',
    file_hash TEXT NOT NULL DEFAULT '',
    delivery TEXT NOT NULL DEFAULT 'file',
    auto_installed BOOLEAN NOT NULL DEFAULT 0,
    depends_on TEXT,
    pack_id TEXT,
    metadata JSON NOT NULL DEFAULT '{}',
    installed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_installed_mods_gameserver ON installed_mods(gameserver_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_installed_mods_unique ON installed_mods(gameserver_id, source, source_id);
CREATE INDEX IF NOT EXISTS idx_installed_mods_depends ON installed_mods(depends_on) WHERE depends_on IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_installed_mods_pack ON installed_mods(pack_id) WHERE pack_id IS NOT NULL;

CREATE TABLE pack_exclusions (
    id TEXT PRIMARY KEY,
    pack_mod_id TEXT NOT NULL REFERENCES installed_mods(id) ON DELETE CASCADE,
    source_id TEXT NOT NULL,
    excluded_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_pack_exclusions_unique ON pack_exclusions(pack_mod_id, source_id);

-- Time-series stats for resource usage graphs. Downsampled in tiers:
-- raw (5s) retained 1 hour, 1m averages retained 24 hours, 5m averages retained 7 days.
CREATE TABLE gameserver_stats (
    gameserver_id     TEXT NOT NULL REFERENCES gameservers(id) ON DELETE CASCADE,
    resolution        TEXT NOT NULL DEFAULT 'raw',
    timestamp         DATETIME NOT NULL,
    cpu_percent       REAL NOT NULL,
    memory_usage_mb   INTEGER NOT NULL,
    memory_limit_mb   INTEGER NOT NULL,
    volume_size_bytes INTEGER NOT NULL DEFAULT 0,
    players_online    INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_gs_stats_query ON gameserver_stats(gameserver_id, resolution, timestamp);
CREATE INDEX IF NOT EXISTS idx_gs_stats_downsample ON gameserver_stats(resolution, timestamp);
