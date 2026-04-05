package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const workerNodeColumns = "id, name, grpc_address, lan_ip, external_ip, status, max_memory_mb, max_cpu, max_storage_mb, cordoned, tags, port_range_start, port_range_end, sftp_port, last_seen, created_at, updated_at"

type WorkerNodeStore struct {
	db *sql.DB
}

func NewWorkerNodeStore(db *sql.DB) *WorkerNodeStore {
	return &WorkerNodeStore{db: db}
}

func scanWorkerNode(scanner interface{ Scan(...any) error }) (*model.WorkerNode, error) {
	var n model.WorkerNode
	err := scanner.Scan(&n.ID, &n.Name, &n.GRPCAddress, &n.LanIP, &n.ExternalIP, &n.Status, &n.MaxMemoryMB, &n.MaxCPU, &n.MaxStorageMB, &n.Cordoned, &n.Tags, &n.PortRangeStart, &n.PortRangeEnd, &n.SFTPPort, &n.LastSeen, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// UpsertWorkerNode inserts or updates a worker node's IP, status, and last_seen fields.
func (s *WorkerNodeStore) UpsertWorkerNode(node *model.WorkerNode) error {
	now := time.Now()
	status := node.Status
	if status == "" {
		status = model.WorkerStatusOffline
	}
	_, err := s.db.Exec(`
		INSERT INTO worker_nodes (id, name, grpc_address, lan_ip, external_ip, status, last_seen, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE name END,
			grpc_address = CASE WHEN excluded.grpc_address != '' THEN excluded.grpc_address ELSE grpc_address END,
			lan_ip = excluded.lan_ip,
			external_ip = excluded.external_ip,
			status = excluded.status,
			last_seen = excluded.last_seen,
			updated_at = excluded.updated_at`,
		node.ID, node.Name, node.GRPCAddress, node.LanIP, node.ExternalIP, status, now, now, now,
	)
	if err != nil {
		return fmt.Errorf("upserting worker node %s: %w", node.ID, err)
	}
	return nil
}

func (s *WorkerNodeStore) GetWorkerNode(id string) (*model.WorkerNode, error) {
	row := s.db.QueryRow(
		"SELECT "+workerNodeColumns+" FROM worker_nodes WHERE id = ?",
		id,
	)
	n, err := scanWorkerNode(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting worker node %s: %w", id, err)
	}
	return n, nil
}

func (s *WorkerNodeStore) ListWorkerNodes() ([]model.WorkerNode, error) {
	rows, err := s.db.Query("SELECT " + workerNodeColumns + " FROM worker_nodes ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("listing worker nodes: %w", err)
	}
	defer rows.Close()

	var nodes []model.WorkerNode
	for rows.Next() {
		var n model.WorkerNode
		if err := rows.Scan(&n.ID, &n.Name, &n.GRPCAddress, &n.LanIP, &n.ExternalIP, &n.Status, &n.MaxMemoryMB, &n.MaxCPU, &n.MaxStorageMB, &n.Cordoned, &n.Tags, &n.PortRangeStart, &n.PortRangeEnd, &n.SFTPPort, &n.LastSeen, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scanning worker node row: %w", err)
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// updateWorkerNode updates columns on a worker node by ID.
// Returns an error if the node doesn't exist.
func (s *WorkerNodeStore) updateWorkerNode(id, query string, args ...any) error {
	args = append(args, time.Now(), id)
	result, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("updating worker node %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for worker node %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("worker node %s not found", id)
	}
	return nil
}

func (s *WorkerNodeStore) SetWorkerNodeStatus(id string, status string) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET status = ?, updated_at = ? WHERE id = ?", status)
}

// ResetAllWorkerStatus sets all worker nodes to the given status.
// Used on controller startup to mark all workers offline until they reconnect.
func (s *WorkerNodeStore) ResetAllWorkerStatus(status string) error {
	_, err := s.db.Exec("UPDATE worker_nodes SET status = ?, updated_at = ?", status, time.Now())
	if err != nil {
		return fmt.Errorf("resetting all worker status to %s: %w", status, err)
	}
	return nil
}

func (s *WorkerNodeStore) SetWorkerNodeName(id string, name string) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET name = ?, updated_at = ? WHERE id = ?", name)
}

func (s *WorkerNodeStore) SetWorkerNodeSFTPPort(id string, sftpPort int) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET sftp_port = ?, updated_at = ? WHERE id = ?", sftpPort)
}

func (s *WorkerNodeStore) SetWorkerNodeCordoned(id string, cordoned bool) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET cordoned = ?, updated_at = ? WHERE id = ?", cordoned)
}

func (s *WorkerNodeStore) SetWorkerNodeTags(id string, tags model.Labels) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET tags = ?, updated_at = ? WHERE id = ?", tags)
}

func (s *WorkerNodeStore) SetWorkerNodePortRange(id string, start *int, end *int) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET port_range_start = ?, port_range_end = ?, updated_at = ? WHERE id = ?", start, end)
}

func (s *WorkerNodeStore) SetWorkerNodeLimits(id string, maxMemoryMB *int, maxCPU *float64, maxStorageMB *int) error {
	return s.updateWorkerNode(id, "UPDATE worker_nodes SET max_memory_mb = ?, max_cpu = ?, max_storage_mb = ?, updated_at = ? WHERE id = ?", maxMemoryMB, maxCPU, maxStorageMB)
}
