package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Gameserver struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	GameID        string          `json:"game_id"`
	Ports         json.RawMessage `json:"ports"`
	Env           json.RawMessage `json:"env"`
	MemoryLimitMB  int             `json:"memory_limit_mb"`
	CPULimit       float64         `json:"cpu_limit"`
	CPUEnforced    bool            `json:"cpu_enforced"`
	ContainerID    *string         `json:"container_id"`
	VolumeName     string          `json:"volume_name"`
	Status         string          `json:"status"`
	ErrorReason    string          `json:"error_reason"`
	PortMode       string          `json:"port_mode"`
	NodeID         *string         `json:"node_id"`
	SFTPUsername   string          `json:"sftp_username"`
	HashedSFTPPassword string      `json:"-"`
	Installed      bool            `json:"installed"`
	BackupLimit    *int            `json:"backup_limit"`
	StorageLimitMB *int            `json:"storage_limit_mb"`
	NodeTags       string          `json:"node_tags"`
	AutoRestart    bool            `json:"auto_restart"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

const gameserverColumns = "id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, created_at, updated_at"

type GameserverFilter struct {
	GameID *string
	Status *string
	NodeID *string
	IDs    []string // restrict results to these IDs (used for scoped token filtering)
	Pagination
}

func ListGameservers(db *sql.DB, filter GameserverFilter) ([]Gameserver, error) {
	query := "SELECT " + gameserverColumns + " FROM gameservers WHERE 1=1"
	var args []any

	if filter.GameID != nil {
		query += " AND game_id = ?"
		args = append(args, *filter.GameID)
	}
	if filter.Status != nil {
		query += " AND status = ?"
		args = append(args, *filter.Status)
	}
	if filter.NodeID != nil {
		if *filter.NodeID == "" {
			query += " AND node_id IS NULL"
		} else {
			query += " AND node_id = ?"
			args = append(args, *filter.NodeID)
		}
	}
	if len(filter.IDs) > 0 {
		placeholders := strings.Repeat("?,", len(filter.IDs))
		placeholders = placeholders[:len(placeholders)-1]
		query += " AND id IN (" + placeholders + ")"
		for _, id := range filter.IDs {
			args = append(args, id)
		}
	}
	query += " ORDER BY name"
	query = filter.Pagination.ApplyToQuery(query, 0)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing gameservers: %w", err)
	}
	defer rows.Close()

	var gameservers []Gameserver
	for rows.Next() {
		gs, err := scanGameserver(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning gameserver row: %w", err)
		}
		gameservers = append(gameservers, gs)
	}
	return gameservers, rows.Err()
}

func GetGameserver(db *sql.DB, id string) (*Gameserver, error) {
	row := db.QueryRow("SELECT "+gameserverColumns+" FROM gameservers WHERE id = ?", id)
	gs, err := scanGameserver(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", id, err)
	}
	return &gs, nil
}

// scanGameserver handles scanning JSON columns via string intermediaries
// since SQLite drivers return JSON columns as strings, not []byte.
func scanGameserver(scan func(dest ...any) error) (Gameserver, error) {
	var gs Gameserver
	var portsStr, envStr string
	err := scan(&gs.ID, &gs.Name, &gs.GameID, &portsStr, &envStr, &gs.MemoryLimitMB, &gs.CPULimit, &gs.CPUEnforced, &gs.ContainerID, &gs.VolumeName, &gs.Status, &gs.ErrorReason, &gs.PortMode, &gs.NodeID, &gs.SFTPUsername, &gs.HashedSFTPPassword, &gs.Installed, &gs.BackupLimit, &gs.StorageLimitMB, &gs.NodeTags, &gs.AutoRestart, &gs.CreatedAt, &gs.UpdatedAt)
	if err != nil {
		return gs, err
	}
	gs.Ports = json.RawMessage(portsStr)
	gs.Env = json.RawMessage(envStr)
	return gs, nil
}

func CreateGameserver(db *sql.DB, gs *Gameserver) error {
	now := time.Now()
	gs.CreatedAt = now
	gs.UpdatedAt = now

	_, err := db.Exec(
		"INSERT INTO gameservers (id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		gs.ID, gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.ContainerID, gs.VolumeName, gs.Status, gs.ErrorReason, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.CreatedAt, gs.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating gameserver %s: %w", gs.ID, err)
	}
	return nil
}

func UpdateGameserver(db *sql.DB, gs *Gameserver) error {
	gs.UpdatedAt = time.Now()

	result, err := db.Exec(
		"UPDATE gameservers SET name = ?, game_id = ?, ports = ?, env = ?, memory_limit_mb = ?, cpu_limit = ?, cpu_enforced = ?, container_id = ?, volume_name = ?, status = ?, error_reason = ?, port_mode = ?, node_id = ?, sftp_username = ?, hashed_sftp_password = ?, installed = ?, backup_limit = ?, storage_limit_mb = ?, node_tags = ?, auto_restart = ?, updated_at = ? WHERE id = ?",
		gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.ContainerID, gs.VolumeName, gs.Status, gs.ErrorReason, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.UpdatedAt, gs.ID,
	)
	if err != nil {
		return fmt.Errorf("updating gameserver %s: %w", gs.ID, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for gameserver %s: %w", gs.ID, err)
	}
	if rows == 0 {
		return fmt.Errorf("gameserver %s not found", gs.ID)
	}
	return nil
}

func GetGameserverBySFTPUsername(db *sql.DB, username string) (*Gameserver, error) {
	row := db.QueryRow("SELECT "+gameserverColumns+" FROM gameservers WHERE sftp_username = ?", username)
	gs, err := scanGameserver(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting gameserver by sftp username %s: %w", username, err)
	}
	return &gs, nil
}

// AllocatedMemoryByNode returns the total memory_limit_mb allocated to gameservers on a node.
func AllocatedMemoryByNode(db *sql.DB, nodeID string) (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(memory_limit_mb), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated memory for node %s: %w", nodeID, err)
	}
	return total, nil
}

// AllocatedCPUByNode returns the total cpu_limit allocated to gameservers on a node.
func AllocatedCPUByNode(db *sql.DB, nodeID string) (float64, error) {
	var total float64
	err := db.QueryRow("SELECT COALESCE(SUM(cpu_limit), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated CPU for node %s: %w", nodeID, err)
	}
	return total, nil
}

// AllocatedStorageByNode returns the total storage_limit_mb allocated to gameservers on a node.
func AllocatedStorageByNode(db *sql.DB, nodeID string) (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(storage_limit_mb), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated storage for node %s: %w", nodeID, err)
	}
	return total, nil
}

// Excluding variants — used by auto-migration to check capacity without counting the gameserver being updated.

func AllocatedMemoryByNodeExcluding(db *sql.DB, nodeID, excludeID string) (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(memory_limit_mb), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated memory for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

func AllocatedCPUByNodeExcluding(db *sql.DB, nodeID, excludeID string) (float64, error) {
	var total float64
	err := db.QueryRow("SELECT COALESCE(SUM(cpu_limit), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated CPU for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

func AllocatedStorageByNodeExcluding(db *sql.DB, nodeID, excludeID string) (int, error) {
	var total int
	err := db.QueryRow("SELECT COALESCE(SUM(storage_limit_mb), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated storage for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

func DeleteGameserver(db *sql.DB, id string) error {
	result, err := db.Exec("DELETE FROM gameservers WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting gameserver %s: %w", id, err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for gameserver %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("gameserver %s not found", id)
	}
	return nil
}
