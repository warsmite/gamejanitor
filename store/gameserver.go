package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

type GameserverStore struct {
	db *sql.DB
}

func NewGameserverStore(db *sql.DB) *GameserverStore {
	return &GameserverStore{db: db}
}

const gameserverColumns = "id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, container_id, volume_name, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, connection_address, applied_config, created_at, updated_at"

// scanGameserver handles scanning JSON columns via sql.Scanner (model.Ports, model.Env, model.Labels).
// Status and ErrorReason are not in the gameservers table — they are derived from
// the latest status_changed activity via PopulateStatus/PopulateStatuses.
func scanGameserver(scan func(dest ...any) error) (model.Gameserver, error) {
	var gs model.Gameserver
	var appliedConfig model.AppliedConfig
	err := scan(&gs.ID, &gs.Name, &gs.GameID, &gs.Ports, &gs.Env, &gs.MemoryLimitMB, &gs.CPULimit, &gs.CPUEnforced, &gs.ContainerID, &gs.VolumeName, &gs.PortMode, &gs.NodeID, &gs.SFTPUsername, &gs.HashedSFTPPassword, &gs.Installed, &gs.BackupLimit, &gs.StorageLimitMB, &gs.NodeTags, &gs.AutoRestart, &gs.ConnectionAddress, &appliedConfig, &gs.CreatedAt, &gs.UpdatedAt)
	if appliedConfig.Env != nil {
		gs.AppliedConfig = &appliedConfig
	}
	if err != nil {
		return gs, err
	}
	// Default status until PopulateStatus is called
	gs.Status = "stopped"
	return gs, nil
}

func (s *GameserverStore) ListGameservers(filter model.GameserverFilter) ([]model.Gameserver, error) {
	query := "SELECT " + gameserverColumns + " FROM gameservers WHERE 1=1"
	var args []any

	if filter.GameID != nil {
		query += " AND game_id = ?"
		args = append(args, *filter.GameID)
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

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing gameservers: %w", err)
	}
	defer rows.Close()

	var gameservers []model.Gameserver
	for rows.Next() {
		gs, err := scanGameserver(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning gameserver row: %w", err)
		}
		gameservers = append(gameservers, gs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	s.PopulateStatuses(gameservers)

	// Status filter is applied post-query since status is derived from the activity table
	if filter.Status != nil {
		filtered := gameservers[:0]
		for _, gs := range gameservers {
			if gs.Status == *filter.Status {
				filtered = append(filtered, gs)
			}
		}
		gameservers = filtered
	}

	return gameservers, nil
}

func (s *GameserverStore) GetGameserver(id string) (*model.Gameserver, error) {
	row := s.db.QueryRow("SELECT "+gameserverColumns+" FROM gameservers WHERE id = ?", id)
	gs, err := scanGameserver(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting gameserver %s: %w", id, err)
	}
	s.PopulateStatus(&gs)
	return &gs, nil
}

func (s *GameserverStore) CreateGameserver(gs *model.Gameserver) error {
	now := time.Now()
	gs.CreatedAt = now
	gs.UpdatedAt = now

	_, err := s.db.Exec(
		"INSERT INTO gameservers (id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, container_id, volume_name, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, connection_address, applied_config, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		gs.ID, gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.ContainerID, gs.VolumeName, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.ConnectionAddress, gs.AppliedConfig, gs.CreatedAt, gs.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating gameserver %s: %w", gs.ID, err)
	}
	return nil
}

func (s *GameserverStore) UpdateGameserver(gs *model.Gameserver) error {
	gs.UpdatedAt = time.Now()

	result, err := s.db.Exec(
		"UPDATE gameservers SET name = ?, game_id = ?, ports = ?, env = ?, memory_limit_mb = ?, cpu_limit = ?, cpu_enforced = ?, container_id = ?, volume_name = ?, port_mode = ?, node_id = ?, sftp_username = ?, hashed_sftp_password = ?, installed = ?, backup_limit = ?, storage_limit_mb = ?, node_tags = ?, auto_restart = ?, connection_address = ?, applied_config = ?, updated_at = ? WHERE id = ?",
		gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.ContainerID, gs.VolumeName, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.ConnectionAddress, gs.AppliedConfig, gs.UpdatedAt, gs.ID,
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

func (s *GameserverStore) DeleteGameserver(id string) error {
	result, err := s.db.Exec("DELETE FROM gameservers WHERE id = ?", id)
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

func (s *GameserverStore) GetGameserverBySFTPUsername(username string) (*model.Gameserver, error) {
	row := s.db.QueryRow("SELECT "+gameserverColumns+" FROM gameservers WHERE sftp_username = ?", username)
	gs, err := scanGameserver(row.Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting gameserver by sftp username %s: %w", username, err)
	}
	s.PopulateStatus(&gs)
	return &gs, nil
}

// AllocatedMemoryByNode returns the total memory_limit_mb allocated to gameservers on a node.
func (s *GameserverStore) AllocatedMemoryByNode(nodeID string) (int, error) {
	var total int
	err := s.db.QueryRow("SELECT COALESCE(SUM(memory_limit_mb), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated memory for node %s: %w", nodeID, err)
	}
	return total, nil
}

// AllocatedCPUByNode returns the total cpu_limit allocated to gameservers on a node.
func (s *GameserverStore) AllocatedCPUByNode(nodeID string) (float64, error) {
	var total float64
	err := s.db.QueryRow("SELECT COALESCE(SUM(cpu_limit), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated CPU for node %s: %w", nodeID, err)
	}
	return total, nil
}

// AllocatedStorageByNode returns the total storage_limit_mb allocated to gameservers on a node.
func (s *GameserverStore) AllocatedStorageByNode(nodeID string) (int, error) {
	var total int
	err := s.db.QueryRow("SELECT COALESCE(SUM(storage_limit_mb), 0) FROM gameservers WHERE node_id = ?", nodeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated storage for node %s: %w", nodeID, err)
	}
	return total, nil
}

// Excluding variants — used by auto-migration to check capacity without counting the gameserver being updated.

func (s *GameserverStore) AllocatedMemoryByNodeExcluding(nodeID, excludeID string) (int, error) {
	var total int
	err := s.db.QueryRow("SELECT COALESCE(SUM(memory_limit_mb), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated memory for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

func (s *GameserverStore) AllocatedCPUByNodeExcluding(nodeID, excludeID string) (float64, error) {
	var total float64
	err := s.db.QueryRow("SELECT COALESCE(SUM(cpu_limit), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated CPU for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

func (s *GameserverStore) AllocatedStorageByNodeExcluding(nodeID, excludeID string) (int, error) {
	var total int
	err := s.db.QueryRow("SELECT COALESCE(SUM(storage_limit_mb), 0) FROM gameservers WHERE node_id = ? AND id != ?", nodeID, excludeID).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("querying allocated storage for node %s excluding %s: %w", nodeID, excludeID, err)
	}
	return total, nil
}

// PopulateStatus derives a gameserver's status and error_reason from the latest
// status_changed activity. If no status_changed activity exists, defaults to "stopped".
func (s *GameserverStore) PopulateStatus(gs *model.Gameserver) {
	var newStatus, errorReason string
	err := s.db.QueryRow(
		`SELECT json_extract(data, '$.new_status'), COALESCE(json_extract(data, '$.error_reason'), '')
		 FROM activity WHERE gameserver_id = ? AND type = 'status_changed'
		 ORDER BY started_at DESC LIMIT 1`, gs.ID).Scan(&newStatus, &errorReason)
	if err != nil || newStatus == "" {
		gs.Status = "stopped"
		gs.ErrorReason = ""
		return
	}
	gs.Status = newStatus
	gs.ErrorReason = errorReason
}

// PopulateStatuses derives status for a slice of gameservers in a single batch query.
func (s *GameserverStore) PopulateStatuses(gameservers []model.Gameserver) {
	if len(gameservers) == 0 {
		return
	}

	// Build a map for O(1) lookup
	byID := make(map[string]*model.Gameserver, len(gameservers))
	placeholders := make([]string, len(gameservers))
	args := make([]any, len(gameservers))
	for i := range gameservers {
		byID[gameservers[i].ID] = &gameservers[i]
		placeholders[i] = "?"
		args[i] = gameservers[i].ID
	}

	// Single query: get the latest status_changed activity per gameserver
	query := `SELECT gameserver_id, json_extract(data, '$.new_status'), COALESCE(json_extract(data, '$.error_reason'), '')
		FROM activity a1
		WHERE a1.type = 'status_changed'
		AND a1.gameserver_id IN (` + strings.Join(placeholders, ",") + `)
		AND a1.started_at = (
			SELECT MAX(a2.started_at) FROM activity a2
			WHERE a2.gameserver_id = a1.gameserver_id AND a2.type = 'status_changed'
		)`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		// Fall back to individual queries
		for i := range gameservers {
			s.PopulateStatus(&gameservers[i])
		}
		return
	}
	defer rows.Close()

	for rows.Next() {
		var gsID, newStatus, errorReason string
		if err := rows.Scan(&gsID, &newStatus, &errorReason); err != nil {
			continue
		}
		if gs, ok := byID[gsID]; ok && newStatus != "" {
			gs.Status = newStatus
			gs.ErrorReason = errorReason
		}
	}
}

// PopulateNode resolves the node data from the worker_nodes table.
func (s *GameserverStore) PopulateNode(gs *model.Gameserver) {
	if gs.NodeID == nil || *gs.NodeID == "" {
		return
	}
	wns := NewWorkerNodeStore(s.db)
	node, err := wns.GetWorkerNode(*gs.NodeID)
	if err != nil || node == nil {
		return
	}
	gs.Node = &model.GameserverNode{
		ExternalIP: node.ExternalIP,
		LanIP:      node.LanIP,
	}
}

// PopulateNodes resolves node data for a slice of gameservers.
func (s *GameserverStore) PopulateNodes(gameservers []model.Gameserver) {
	// Batch: collect unique node IDs, query once each
	wns := NewWorkerNodeStore(s.db)
	seen := make(map[string]*model.GameserverNode)
	for i := range gameservers {
		gs := &gameservers[i]
		if gs.NodeID == nil || *gs.NodeID == "" {
			continue
		}
		nid := *gs.NodeID
		if n, ok := seen[nid]; ok {
			gs.Node = n
			continue
		}
		node, err := wns.GetWorkerNode(nid)
		if err != nil || node == nil {
			seen[nid] = nil
			continue
		}
		n := &model.GameserverNode{ExternalIP: node.ExternalIP, LanIP: node.LanIP}
		seen[nid] = n
		gs.Node = n
	}
}
