package models

import (
	"database/sql"
	"fmt"
	"time"
)

type Backup struct {
	ID           string    `json:"id"`
	GameserverID string    `json:"gameserver_id"`
	Name         string    `json:"name"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
}

func ListBackups(db *sql.DB, gameserverID string) ([]Backup, error) {
	rows, err := db.Query("SELECT id, gameserver_id, name, size_bytes, created_at FROM backups WHERE gameserver_id = ? ORDER BY created_at DESC", gameserverID)
	if err != nil {
		return nil, fmt.Errorf("listing backups: %w", err)
	}
	defer rows.Close()

	var backups []Backup
	for rows.Next() {
		var b Backup
		if err := rows.Scan(&b.ID, &b.GameserverID, &b.Name, &b.SizeBytes, &b.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning backup row: %w", err)
		}
		backups = append(backups, b)
	}
	return backups, rows.Err()
}

func GetBackup(db *sql.DB, id string) (*Backup, error) {
	var b Backup
	err := db.QueryRow("SELECT id, gameserver_id, name, size_bytes, created_at FROM backups WHERE id = ?", id).
		Scan(&b.ID, &b.GameserverID, &b.Name, &b.SizeBytes, &b.CreatedAt)
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
		"INSERT INTO backups (id, gameserver_id, name, size_bytes, created_at) VALUES (?, ?, ?, ?, ?)",
		b.ID, b.GameserverID, b.Name, b.SizeBytes, b.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating backup %s: %w", b.ID, err)
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
