package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const operationColumns = "id, gameserver_id, worker_id, type, status, error, metadata, started_at, completed_at"

type OperationStore struct {
	db *sql.DB
}

func NewOperationStore(db *sql.DB) *OperationStore {
	return &OperationStore{db: db}
}

func scanOperation(scanner interface{ Scan(...any) error }) (*model.Operation, error) {
	var op model.Operation
	err := scanner.Scan(&op.ID, &op.GameserverID, &op.WorkerID, &op.Type, &op.Status, &op.Error, &op.Metadata, &op.StartedAt, &op.CompletedAt)
	if err != nil {
		return nil, err
	}
	return &op, nil
}

func (s *OperationStore) CreateOperation(op *model.Operation) error {
	_, err := s.db.Exec(
		"INSERT INTO operations (id, gameserver_id, worker_id, type, status, error, metadata, started_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		op.ID, op.GameserverID, op.WorkerID, op.Type, op.Status, op.Error, op.Metadata, op.StartedAt, op.CompletedAt,
	)
	if err != nil {
		return fmt.Errorf("creating operation %s: %w", op.ID, err)
	}
	return nil
}

func (s *OperationStore) GetOperation(id string) (*model.Operation, error) {
	row := s.db.QueryRow("SELECT "+operationColumns+" FROM operations WHERE id = ?", id)
	op, err := scanOperation(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting operation %s: %w", id, err)
	}
	return op, nil
}

func (s *OperationStore) CompleteOperation(id string) error {
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE operations SET status = ?, completed_at = ? WHERE id = ?",
		model.OperationStatusCompleted, now, id,
	)
	if err != nil {
		return fmt.Errorf("completing operation %s: %w", id, err)
	}
	return nil
}

func (s *OperationStore) FailOperation(id string, errMsg string) error {
	now := time.Now()
	_, err := s.db.Exec(
		"UPDATE operations SET status = ?, error = ?, completed_at = ? WHERE id = ?",
		model.OperationStatusFailed, errMsg, now, id,
	)
	if err != nil {
		return fmt.Errorf("failing operation %s: %w", id, err)
	}
	return nil
}

// AbandonRunningOperations marks all running operations as abandoned.
// Called on controller startup to clean up operations from a previous crash.
func (s *OperationStore) AbandonRunningOperations() (int, error) {
	now := time.Now()
	result, err := s.db.Exec(
		"UPDATE operations SET status = ?, error = 'controller restarted', completed_at = ? WHERE status = ?",
		model.OperationStatusAbandoned, now, model.OperationStatusRunning,
	)
	if err != nil {
		return 0, fmt.Errorf("abandoning running operations: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

// HasRunningOperation returns true if the gameserver has a running operation.
// Used as a mutex to prevent concurrent mutations.
func (s *OperationStore) HasRunningOperation(gameserverID string) (bool, error) {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM operations WHERE gameserver_id = ? AND status = ?",
		gameserverID, model.OperationStatusRunning,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("checking running operations for %s: %w", gameserverID, err)
	}
	return count > 0, nil
}

func (s *OperationStore) ListOperations(filter model.OperationFilter) ([]model.Operation, error) {
	query := "SELECT " + operationColumns + " FROM operations WHERE 1=1"
	var args []any

	if filter.GameserverID != nil {
		query += " AND gameserver_id = ?"
		args = append(args, *filter.GameserverID)
	}
	if filter.Status != nil {
		query += " AND status = ?"
		args = append(args, *filter.Status)
	}
	if filter.WorkerID != nil {
		query += " AND worker_id = ?"
		args = append(args, *filter.WorkerID)
	}

	query += " ORDER BY started_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing operations: %w", err)
	}
	defer rows.Close()

	var ops []model.Operation
	for rows.Next() {
		op, err := scanOperation(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning operation: %w", err)
		}
		ops = append(ops, *op)
	}
	return ops, rows.Err()
}

// PruneOperations deletes completed/failed/abandoned operations older than the given number of days.
func (s *OperationStore) PruneOperations(retentionDays int) (int, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	result, err := s.db.Exec(
		"DELETE FROM operations WHERE status != ? AND completed_at < ?",
		model.OperationStatusRunning, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning operations: %w", err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}
