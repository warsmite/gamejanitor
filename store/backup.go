package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

type BackupStore struct {
	db *sql.DB
}

func NewBackupStore(db *sql.DB) *BackupStore {
	return &BackupStore{db: db}
}

var backupColumns = "id, gameserver_id, name, size_bytes, status, error_reason, created_at"

func scanBackup(row interface{ Scan(dest ...any) error }) (model.Backup, error) {
	var b model.Backup
	err := row.Scan(&b.ID, &b.GameserverID, &b.Name, &b.SizeBytes, &b.Status, &b.ErrorReason, &b.CreatedAt)
	return b, err
}

func (s *BackupStore) ListBackups(filter model.BackupFilter) ([]model.Backup, error) {
	query := "SELECT id, gameserver_id, name, size_bytes, status, error_reason, created_at FROM backups WHERE gameserver_id = ? ORDER BY created_at DESC"
	query = filter.Pagination.ApplyToQuery(query, 0)
	rows, err := s.db.Query(query, filter.GameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing backups: %w", err)
	}
	defer rows.Close()

	var backups []model.Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning backup row: %w", err)
		}
		backups = append(backups, b)
	}
	return backups, rows.Err()
}

func (s *BackupStore) GetBackup(id string) (*model.Backup, error) {
	b, err := scanBackup(s.db.QueryRow("SELECT id, gameserver_id, name, size_bytes, status, error_reason, created_at FROM backups WHERE id = ?", id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting backup %s: %w", id, err)
	}
	return &b, nil
}

func (s *BackupStore) CreateBackup(b *model.Backup) error {
	b.CreatedAt = time.Now()

	_, err := s.db.Exec(
		"INSERT INTO backups (id, gameserver_id, name, size_bytes, status, error_reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		b.ID, b.GameserverID, b.Name, b.SizeBytes, b.Status, b.ErrorReason, b.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating backup %s: %w", b.ID, err)
	}
	return nil
}

func (s *BackupStore) UpdateBackupStatus(id string, status string, sizeBytes int64, errorReason string) error {
	_, err := s.db.Exec(
		"UPDATE backups SET status = ?, size_bytes = ?, error_reason = ? WHERE id = ?",
		status, sizeBytes, errorReason, id,
	)
	if err != nil {
		return fmt.Errorf("updating backup %s status: %w", id, err)
	}
	return nil
}

func (s *BackupStore) DeleteBackupsByGameserver(gameserverID string) error {
	_, err := s.db.Exec("DELETE FROM backups WHERE gameserver_id = ?", gameserverID)
	if err != nil {
		return fmt.Errorf("deleting backups for gameserver %s: %w", gameserverID, err)
	}
	return nil
}

func (s *BackupStore) TotalBackupSizeByGameserver(gameserverID string) (int64, error) {
	var total int64
	err := s.db.QueryRow("SELECT COALESCE(SUM(size_bytes), 0) FROM backups WHERE gameserver_id = ?", gameserverID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying total backup size for gameserver %s: %w", gameserverID, err)
	}
	return total, nil
}

func (s *BackupStore) DeleteBackup(id string) error {
	result, err := s.db.Exec("DELETE FROM backups WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting backup %s: %w", id, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for backup %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("backup %s not found", id)
	}
	return nil
}
