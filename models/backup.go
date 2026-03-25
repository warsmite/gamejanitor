package models

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	BackupStatusInProgress = "in_progress"
	BackupStatusCompleted  = "completed"
	BackupStatusFailed     = "failed"
)

type Backup struct {
	ID           string    `json:"id"`
	GameserverID string    `json:"gameserver_id"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"size_bytes"`
	Status       string    `json:"status"`
	ErrorReason  string    `json:"error_reason,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type BackupFilter struct {
	GameserverID string
	Pagination
}

func ListBackups(db *sql.DB, filter BackupFilter) ([]Backup, error) {
	query := "SELECT id, gameserver_id, name, size_bytes, status, error_reason, created_at FROM backups WHERE gameserver_id = ? ORDER BY created_at DESC"
	query = filter.Pagination.ApplyToQuery(query, 0)
	rows, err := db.Query(query, filter.GameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing backups: %w", err)
	}
	defer rows.Close()

	var backups []Backup
	for rows.Next() {
		var b Backup
		if err := rows.Scan(&b.ID, &b.GameserverID, &b.Name, &b.SizeBytes, &b.Status, &b.ErrorReason, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning backup row: %w", err)
		}
		backups = append(backups, b)
	}
	return backups, rows.Err()
}

func GetBackup(db *sql.DB, id string) (*Backup, error) {
	var b Backup
	err := db.QueryRow("SELECT id, gameserver_id, name, size_bytes, status, error_reason, created_at FROM backups WHERE id = ?", id).
		Scan(&b.ID, &b.GameserverID, &b.Name, &b.SizeBytes, &b.Status, &b.ErrorReason, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting backup %s: %w", id, err)
	}
	return &b, nil
}

func CreateBackup(db *sql.DB, b *Backup) error {
	b.CreatedAt = time.Now()

	_, err := db.Exec(
		"INSERT INTO backups (id, gameserver_id, name, size_bytes, status, error_reason, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		b.ID, b.GameserverID, b.Name, b.SizeBytes, b.Status, b.ErrorReason, b.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating backup %s: %w", b.ID, err)
	}
	return nil
}

func UpdateBackupStatus(db *sql.DB, id string, status string, sizeBytes int64, errorReason string) error {
	_, err := db.Exec(
		"UPDATE backups SET status = ?, size_bytes = ?, error_reason = ? WHERE id = ?",
		status, sizeBytes, errorReason, id,
	)
	if err != nil {
		return fmt.Errorf("updating backup %s status: %w", id, err)
	}
	return nil
}

func DeleteBackupsByGameserver(db *sql.DB, gameserverID string) error {
	_, err := db.Exec("DELETE FROM backups WHERE gameserver_id = ?", gameserverID)
	if err != nil {
		return fmt.Errorf("deleting backups for gameserver %s: %w", gameserverID, err)
	}
	return nil
}

func TotalBackupSizeByGameserver(db *sql.DB, gameserverID string) (int64, error) {
	var total int64
	err := db.QueryRow("SELECT COALESCE(SUM(size_bytes), 0) FROM backups WHERE gameserver_id = ?", gameserverID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying total backup size for gameserver %s: %w", gameserverID, err)
	}
	return total, nil
}

func DeleteBackup(db *sql.DB, id string) error {
	result, err := db.Exec("DELETE FROM backups WHERE id = ?", id)
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
