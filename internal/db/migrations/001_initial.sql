CREATE TABLE gameservers (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    game_id TEXT NOT NULL,
    ports JSON NOT NULL DEFAULT '[]',
    env JSON NOT NULL DEFAULT '{}',
    memory_limit_mb INTEGER NOT NULL DEFAULT 0,
    cpu_limit REAL NOT NULL DEFAULT 0,
    container_id TEXT,
    volume_name TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'stopped',
    error_reason TEXT NOT NULL DEFAULT '',
    port_mode TEXT NOT NULL DEFAULT 'auto',
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
    file_path TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_gameservers_game_id ON gameservers(game_id);
CREATE INDEX IF NOT EXISTS idx_schedules_gameserver_id ON schedules(gameserver_id);
CREATE INDEX IF NOT EXISTS idx_backups_gameserver_id ON backups(gameserver_id);
