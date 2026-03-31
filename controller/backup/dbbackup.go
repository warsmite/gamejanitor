package backup

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"time"
)

// DBBackupScheduler periodically backs up the SQLite database to the backup store.
type DBBackupScheduler struct {
	db       *sql.DB
	store    Storage
	interval time.Duration
	retain   int
	log      *slog.Logger
}

// NewDBBackupScheduler creates a scheduler that backs up the DB at the given interval,
// keeping the specified number of recent backups.
func NewDBBackupScheduler(db *sql.DB, store Storage, intervalHours int, retain int, log *slog.Logger) *DBBackupScheduler {
	if retain <= 0 {
		retain = 3
	}
	return &DBBackupScheduler{
		db:       db,
		store:    store,
		interval: time.Duration(intervalHours) * time.Hour,
		retain:   retain,
		log:      log,
	}
}

// Start runs an immediate backup then ticks at the configured interval.
// Blocks until ctx is canceled.
func (s *DBBackupScheduler) Start(ctx context.Context) {
	s.run()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.run()
		}
	}
}

func (s *DBBackupScheduler) run() {
	backupID := time.Now().UTC().Format("20060102-150405")

	if err := s.backupDB(backupID); err != nil {
		s.log.Error("db backup failed", "error", err)
		return
	}
	s.log.Info("db backup completed", "backup_id", backupID)

	if err := s.pruneOld(); err != nil {
		s.log.Warn("db backup prune failed", "error", err)
	}
}

func (s *DBBackupScheduler) backupDB(backupID string) error {
	// VACUUM INTO creates a clean, standalone copy of the database.
	// It's atomic, doesn't interfere with concurrent reads/writes,
	// and produces a file without WAL dependencies.
	tmpPath := os.TempDir() + "/gamejanitor-db-backup-" + backupID + ".db"
	defer os.Remove(tmpPath)

	if _, err := s.db.Exec("VACUUM INTO ?", tmpPath); err != nil {
		return fmt.Errorf("vacuum into temp file: %w", err)
	}

	f, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("opening temp backup: %w", err)
	}
	defer f.Close()

	if err := s.store.Save(context.Background(), "db", backupID, f); err != nil {
		return fmt.Errorf("saving db backup to store: %w", err)
	}

	return nil
}

func (s *DBBackupScheduler) pruneOld() error {
	// List existing DB backups and remove old ones beyond retention.
	// The storage interface doesn't have a List method, so we use a naming
	// convention: backups are named by timestamp, sorted lexically = chronological.
	lister, ok := s.store.(interface {
		List(ctx context.Context, prefix string) ([]string, error)
	})
	if !ok {
		// Storage doesn't support listing — skip pruning
		return nil
	}

	entries, err := lister.List(context.Background(), "db")
	if err != nil {
		return err
	}

	if len(entries) <= s.retain {
		return nil
	}

	sort.Strings(entries)
	toDelete := entries[:len(entries)-s.retain]
	for _, id := range toDelete {
		if err := s.store.Delete(context.Background(), "db", id); err != nil {
			s.log.Warn("failed to delete old db backup", "id", id, "error", err)
		} else {
			s.log.Info("pruned old db backup", "id", id)
		}
	}
	return nil
}
