CREATE TABLE gameservers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    game_id TEXT NOT NULL,
    ports JSON NOT NULL DEFAULT '[]',
    env JSON NOT NULL DEFAULT '{}',
    memory_limit_mb INTEGER NOT NULL DEFAULT 0,
    cpu_limit REAL NOT NULL DEFAULT 0,
    cpu_enforced INTEGER NOT NULL DEFAULT 0,
    container_id TEXT,
    volume_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'stopped',
    error_reason TEXT NOT NULL DEFAULT '',
    port_mode TEXT NOT NULL DEFAULT 'auto',
    node_id TEXT,
    sftp_username TEXT NOT NULL DEFAULT '',
    hashed_sftp_password TEXT NOT NULL DEFAULT '',
    installed BOOLEAN NOT NULL DEFAULT 0,
    backup_limit INTEGER,
    storage_limit_mb INTEGER,
    auto_restart BOOLEAN NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE schedules (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT NOT NULL REFERENCES gameservers(id),
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    payload JSON NOT NULL DEFAULT '{}',
    enabled BOOLEAN NOT NULL DEFAULT 1,
    last_run DATETIME,
    next_run DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE backups (
    id TEXT PRIMARY KEY,
    gameserver_id TEXT NOT NULL REFERENCES gameservers(id),
    name TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE tokens (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    hashed_token TEXT NOT NULL,
    scope TEXT NOT NULL DEFAULT 'gameserver',
    gameserver_ids JSON NOT NULL DEFAULT '[]',
    permissions JSON NOT NULL DEFAULT '[]',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    last_used_at DATETIME,
    expires_at DATETIME
);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE worker_nodes (
    id TEXT PRIMARY KEY,
    lan_ip TEXT NOT NULL DEFAULT '',
    external_ip TEXT NOT NULL DEFAULT '',
    port_range_start INTEGER,
    port_range_end INTEGER,
    max_memory_mb INTEGER,
    max_cpu REAL,
    max_storage_mb INTEGER,
    cordoned BOOLEAN NOT NULL DEFAULT 0,
    sftp_port INTEGER NOT NULL DEFAULT 0,
    last_seen DATETIME,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE audit_log (
    id TEXT PRIMARY KEY,
    timestamp DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    action TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL DEFAULT '',
    token_id TEXT NOT NULL DEFAULT '',
    token_name TEXT NOT NULL DEFAULT '',
    ip_address TEXT NOT NULL DEFAULT '',
    status_code INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_gameservers_game_id ON gameservers(game_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gameservers_sftp_username ON gameservers(sftp_username) WHERE sftp_username != '';
CREATE INDEX IF NOT EXISTS idx_schedules_gameserver_id ON schedules(gameserver_id);
CREATE INDEX IF NOT EXISTS idx_backups_gameserver_id ON backups(gameserver_id);
CREATE INDEX IF NOT EXISTS idx_audit_log_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_log_action ON audit_log(action);
CREATE INDEX IF NOT EXISTS idx_audit_log_resource ON audit_log(resource_type, resource_id);

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
