package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Gameserver struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	GameID        string          `json:"game_id"`
	Ports         json.RawMessage `json:"ports"`
	Env           json.RawMessage `json:"env"`
	MemoryLimitMB int             `json:"memory_limit_mb"`
	CPULimit      float64         `json:"cpu_limit"`
	ContainerID   *string         `json:"container_id"`
	VolumeName    string          `json:"volume_name"`
	Status        string          `json:"status"`
	ErrorReason   string          `json:"error_reason"`
	PortMode      string          `json:"port_mode"`
	NodeID        *string         `json:"node_id"`
	SFTPUsername  string          `json:"sftp_username"`
	SFTPPassword  string          `json:"sftp_password"`
	MaxMemoryMB   *int            `json:"max_memory_mb"`
	MaxCPU        *float64        `json:"max_cpu"`
	MaxBackups    *int            `json:"max_backups"`
	MaxStorageMB  *int            `json:"max_storage_mb"`
	CreatedAt     time.Time       `json:"created_at"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

type GameserverFilter struct {
	GameID *string
	Status *string
	NodeID *string
}

func ListGameservers(db *sql.DB, filter GameserverFilter) ([]Gameserver, error) {
	query := "SELECT id, name, game_id, ports, env, memory_limit_mb, cpu_limit, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, sftp_password, max_memory_mb, max_cpu, max_backups, max_storage_mb, created_at, updated_at FROM gameservers WHERE 1=1"
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
	query += " ORDER BY name"

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
	row := db.QueryRow("SELECT id, name, game_id, ports, env, memory_limit_mb, cpu_limit, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, sftp_password, max_memory_mb, max_cpu, max_backups, max_storage_mb, created_at, updated_at FROM gameservers WHERE id = ?", id)
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
	err := scan(&gs.ID, &gs.Name, &gs.GameID, &portsStr, &envStr, &gs.MemoryLimitMB, &gs.CPULimit, &gs.ContainerID, &gs.VolumeName, &gs.Status, &gs.ErrorReason, &gs.PortMode, &gs.NodeID, &gs.SFTPUsername, &gs.SFTPPassword, &gs.MaxMemoryMB, &gs.MaxCPU, &gs.MaxBackups, &gs.MaxStorageMB, &gs.CreatedAt, &gs.UpdatedAt)
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
		"INSERT INTO gameservers (id, name, game_id, ports, env, memory_limit_mb, cpu_limit, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, sftp_password, max_memory_mb, max_cpu, max_backups, max_storage_mb, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		gs.ID, gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.ContainerID, gs.VolumeName, gs.Status, gs.ErrorReason, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.SFTPPassword, gs.MaxMemoryMB, gs.MaxCPU, gs.MaxBackups, gs.MaxStorageMB, gs.CreatedAt, gs.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating gameserver %s: %w", gs.ID, err)
	}
	return nil
}

func UpdateGameserver(db *sql.DB, gs *Gameserver) error {
	gs.UpdatedAt = time.Now()

	result, err := db.Exec(
		"UPDATE gameservers SET name = ?, game_id = ?, ports = ?, env = ?, memory_limit_mb = ?, cpu_limit = ?, container_id = ?, volume_name = ?, status = ?, error_reason = ?, port_mode = ?, node_id = ?, sftp_username = ?, sftp_password = ?, max_memory_mb = ?, max_cpu = ?, max_backups = ?, max_storage_mb = ?, updated_at = ? WHERE id = ?",
		gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.ContainerID, gs.VolumeName, gs.Status, gs.ErrorReason, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.SFTPPassword, gs.MaxMemoryMB, gs.MaxCPU, gs.MaxBackups, gs.MaxStorageMB, gs.UpdatedAt, gs.ID,
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
	row := db.QueryRow("SELECT id, name, game_id, ports, env, memory_limit_mb, cpu_limit, container_id, volume_name, status, error_reason, port_mode, node_id, sftp_username, sftp_password, max_memory_mb, max_cpu, max_backups, max_storage_mb, created_at, updated_at FROM gameservers WHERE sftp_username = ?", username)
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
