package store

import "database/sql"

// DB embeds all domain stores, providing a single entry point for database access.
// Controller sub-packages receive *DB and use only the methods their Store interface requires.
type DB struct {
	*GameserverStore
	*BackupStore
	*EventStore
	*ScheduleStore
	*TokenStore
	*WebhookStore
	*WorkerNodeStore
	*ModStore
	*SettingStore
}

func New(db *sql.DB) *DB {
	return &DB{
		GameserverStore: NewGameserverStore(db),
		BackupStore:     NewBackupStore(db),
		EventStore:      NewEventStore(db),
		ScheduleStore:   NewScheduleStore(db),
		TokenStore:      NewTokenStore(db),
		WebhookStore:    NewWebhookStore(db),
		WorkerNodeStore: NewWorkerNodeStore(db),
		ModStore:        NewModStore(db),
		SettingStore:    NewSettingStore(db),
	}
}
