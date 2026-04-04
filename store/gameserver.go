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

const gameserverColumns = "id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, instance_id, volume_name, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, connection_address, applied_config, desired_state, operation, operation_id, created_by_token_id, created_at, updated_at"

func scanGameserver(scan func(dest ...any) error) (model.Gameserver, error) {
	var gs model.Gameserver
	var appliedConfig model.AppliedConfig
	err := scan(&gs.ID, &gs.Name, &gs.GameID, &gs.Ports, &gs.Env, &gs.MemoryLimitMB, &gs.CPULimit, &gs.CPUEnforced, &gs.InstanceID, &gs.VolumeName, &gs.PortMode, &gs.NodeID, &gs.SFTPUsername, &gs.HashedSFTPPassword, &gs.Installed, &gs.BackupLimit, &gs.StorageLimitMB, &gs.NodeTags, &gs.AutoRestart, &gs.ConnectionAddress, &appliedConfig, &gs.DesiredState, &gs.OperationType, &gs.OperationID, &gs.CreatedByTokenID, &gs.CreatedAt, &gs.UpdatedAt)
	if appliedConfig.Env != nil {
		gs.AppliedConfig = &appliedConfig
	}
	return gs, err
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
	// Status is derived at the service layer, not a DB column.
	// filter.Status is applied post-query by the caller.
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
	return &gs, nil
}

func (s *GameserverStore) CreateGameserver(gs *model.Gameserver) error {
	now := time.Now()
	gs.CreatedAt = now
	gs.UpdatedAt = now

	_, err := s.db.Exec(
		"INSERT INTO gameservers (id, name, game_id, ports, env, memory_limit_mb, cpu_limit, cpu_enforced, instance_id, volume_name, port_mode, node_id, sftp_username, hashed_sftp_password, installed, backup_limit, storage_limit_mb, node_tags, auto_restart, connection_address, applied_config, desired_state, operation, operation_id, created_by_token_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		gs.ID, gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.InstanceID, gs.VolumeName, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.ConnectionAddress, gs.AppliedConfig, gs.DesiredState, gs.OperationType, gs.OperationID, gs.CreatedByTokenID, gs.CreatedAt, gs.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating gameserver %s: %w", gs.ID, err)
	}
	return nil
}

func (s *GameserverStore) UpdateGameserver(gs *model.Gameserver) error {
	gs.UpdatedAt = time.Now()

	result, err := s.db.Exec(
		"UPDATE gameservers SET name = ?, game_id = ?, ports = ?, env = ?, memory_limit_mb = ?, cpu_limit = ?, cpu_enforced = ?, instance_id = ?, volume_name = ?, port_mode = ?, node_id = ?, sftp_username = ?, hashed_sftp_password = ?, installed = ?, backup_limit = ?, storage_limit_mb = ?, node_tags = ?, auto_restart = ?, connection_address = ?, applied_config = ?, desired_state = ?, operation = ?, operation_id = ?, updated_at = ? WHERE id = ?",
		gs.Name, gs.GameID, gs.Ports, gs.Env, gs.MemoryLimitMB, gs.CPULimit, gs.CPUEnforced, gs.InstanceID, gs.VolumeName, gs.PortMode, gs.NodeID, gs.SFTPUsername, gs.HashedSFTPPassword, gs.Installed, gs.BackupLimit, gs.StorageLimitMB, gs.NodeTags, gs.AutoRestart, gs.ConnectionAddress, gs.AppliedConfig, gs.DesiredState, gs.OperationType, gs.OperationID, gs.UpdatedAt, gs.ID,
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
	return &gs, nil
}

// ClearStaleOperations resets operation state on all gameservers that have one in progress.
// Called on startup to clean up after a crash.
func (s *GameserverStore) ClearStaleOperations() (int, error) {
	result, err := s.db.Exec("UPDATE gameservers SET operation = NULL, operation_id = NULL WHERE operation IS NOT NULL")
	if err != nil {
		return 0, fmt.Errorf("clearing stale operations: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
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

// GameserverCountByNode returns the number of non-archived gameservers on a node.
func (s *GameserverStore) GameserverCountByNode(nodeID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM gameservers WHERE node_id = ? AND desired_state != 'archived'", nodeID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("querying gameserver count for node %s: %w", nodeID, err)
	}
	return count, nil
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

// --- Token ownership / quota queries ---

// CountGameserversByToken returns the number of gameservers owned by a token.
func (s *GameserverStore) CountGameserversByToken(tokenID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM gameservers WHERE created_by_token_id = ?", tokenID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting gameservers for token %s: %w", tokenID, err)
	}
	return count, nil
}

// SumResourcesByToken returns total memory, CPU, and storage allocated to gameservers owned by a token.
func (s *GameserverStore) SumResourcesByToken(tokenID string) (memoryMB int, cpu float64, storageMB int, err error) {
	err = s.db.QueryRow(
		"SELECT COALESCE(SUM(memory_limit_mb), 0), COALESCE(SUM(cpu_limit), 0), COALESCE(SUM(COALESCE(storage_limit_mb, 0)), 0) FROM gameservers WHERE created_by_token_id = ?",
		tokenID,
	).Scan(&memoryMB, &cpu, &storageMB)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("summing resources for token %s: %w", tokenID, err)
	}
	return
}

// ListGameserverIDsByToken returns IDs of gameservers owned by a token.
func (s *GameserverStore) ListGameserverIDsByToken(tokenID string) ([]string, error) {
	rows, err := s.db.Query("SELECT id FROM gameservers WHERE created_by_token_id = ?", tokenID)
	if err != nil {
		return nil, fmt.Errorf("listing gameserver IDs for token %s: %w", tokenID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning gameserver ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetGameserverOwner returns the created_by_token_id for a gameserver.
// Returns nil if the gameserver doesn't exist or has no owner.
func (s *GameserverStore) GetGameserverOwner(gameserverID string) (*string, error) {
	var owner *string
	err := s.db.QueryRow("SELECT created_by_token_id FROM gameservers WHERE id = ?", gameserverID).Scan(&owner)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting owner for gameserver %s: %w", gameserverID, err)
	}
	return owner, nil
}
