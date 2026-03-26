package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/warsmite/gamejanitor/model"
)

type WebhookStore struct {
	db *sql.DB
}

func NewWebhookStore(db *sql.DB) *WebhookStore {
	return &WebhookStore{db: db}
}

// --- Endpoints ---

func (s *WebhookStore) ListWebhookEndpoints() ([]model.WebhookEndpoint, error) {
	rows, err := s.db.Query(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []model.WebhookEndpoint
	for rows.Next() {
		var e model.WebhookEndpoint
		if err := rows.Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}

func (s *WebhookStore) ListEnabledWebhookEndpoints() ([]model.WebhookEndpoint, error) {
	rows, err := s.db.Query(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []model.WebhookEndpoint
	for rows.Next() {
		var e model.WebhookEndpoint
		if err := rows.Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}

func (s *WebhookStore) GetWebhookEndpoint(id string) (*model.WebhookEndpoint, error) {
	var e model.WebhookEndpoint
	err := s.db.QueryRow(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *WebhookStore) CreateWebhookEndpoint(e *model.WebhookEndpoint) error {
	e.ID = uuid.New().String()
	now := time.Now()
	e.CreatedAt = now
	e.UpdatedAt = now

	_, err := s.db.Exec(`INSERT INTO webhook_endpoints (id, description, url, secret, events, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Description, e.URL, e.Secret, e.Events, e.Enabled, e.CreatedAt, e.UpdatedAt)
	return err
}

func (s *WebhookStore) UpdateWebhookEndpoint(e *model.WebhookEndpoint) error {
	e.UpdatedAt = time.Now()
	res, err := s.db.Exec(`UPDATE webhook_endpoints SET description = ?, url = ?, secret = ?, events = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		e.Description, e.URL, e.Secret, e.Events, e.Enabled, e.UpdatedAt, e.ID)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *WebhookStore) DeleteWebhookEndpoint(id string) error {
	res, err := s.db.Exec(`DELETE FROM webhook_endpoints WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// --- Deliveries ---

func (s *WebhookStore) CreateWebhookDelivery(d *model.WebhookDelivery) error {
	_, err := s.db.Exec(
		`INSERT INTO webhook_deliveries (id, webhook_endpoint_id, event_type, payload, state, attempts, next_attempt_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		d.ID, d.WebhookEndpointID, d.EventType, d.Payload, model.WebhookStatePending, d.NextAttemptAt, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating webhook delivery: %w", err)
	}
	return nil
}

func (s *WebhookStore) ListDeliveriesByEndpoint(endpointID string, state string, limit int) ([]model.WebhookDelivery, error) {
	query := `SELECT id, webhook_endpoint_id, event_type, payload, state, attempts, last_attempt_at, next_attempt_at, last_error, created_at
		 FROM webhook_deliveries WHERE webhook_endpoint_id = ?`
	args := []any{endpointID}

	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying deliveries by endpoint: %w", err)
	}
	defer rows.Close()

	var deliveries []model.WebhookDelivery
	for rows.Next() {
		var d model.WebhookDelivery
		if err := rows.Scan(&d.ID, &d.WebhookEndpointID, &d.EventType, &d.Payload, &d.State, &d.Attempts, &d.LastAttemptAt, &d.NextAttemptAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning webhook delivery row: %w", err)
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (s *WebhookStore) GetPendingDeliveries(limit int) ([]model.WebhookDelivery, error) {
	rows, err := s.db.Query(
		`SELECT d.id, d.webhook_endpoint_id, d.event_type, d.payload, d.state, d.attempts, d.last_attempt_at, d.next_attempt_at, d.last_error, d.created_at
		 FROM webhook_deliveries d
		 WHERE d.state = ? AND d.next_attempt_at <= datetime('now')
		 ORDER BY d.next_attempt_at
		 LIMIT ?`,
		model.WebhookStatePending, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying pending webhook deliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []model.WebhookDelivery
	for rows.Next() {
		var d model.WebhookDelivery
		if err := rows.Scan(&d.ID, &d.WebhookEndpointID, &d.EventType, &d.Payload, &d.State, &d.Attempts, &d.LastAttemptAt, &d.NextAttemptAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning webhook delivery row: %w", err)
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func (s *WebhookStore) MarkDeliverySuccess(id string) error {
	_, err := s.db.Exec(
		`UPDATE webhook_deliveries SET state = ?, attempts = attempts + 1, last_attempt_at = datetime('now') WHERE id = ?`,
		model.WebhookStateDelivered, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s as delivered: %w", id, err)
	}
	return nil
}

func (s *WebhookStore) MarkDeliveryRetry(id string, nextAttemptAt time.Time, lastError string) error {
	_, err := s.db.Exec(
		`UPDATE webhook_deliveries SET attempts = attempts + 1, last_attempt_at = datetime('now'), next_attempt_at = ?, last_error = ? WHERE id = ?`,
		nextAttemptAt, lastError, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s for retry: %w", id, err)
	}
	return nil
}

func (s *WebhookStore) MarkDeliveryFailed(id string, lastError string) error {
	_, err := s.db.Exec(
		`UPDATE webhook_deliveries SET state = ?, attempts = attempts + 1, last_attempt_at = datetime('now'), last_error = ? WHERE id = ?`,
		model.WebhookStateFailed, lastError, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s as failed: %w", id, err)
	}
	return nil
}

func (s *WebhookStore) PruneWebhookDeliveries() (int64, error) {
	result, err := s.db.Exec(
		`DELETE FROM webhook_deliveries WHERE
		 (state = ? AND created_at < datetime('now', '-7 days')) OR
		 (state = ? AND created_at < datetime('now', '-30 days'))`,
		model.WebhookStateDelivered, model.WebhookStateFailed,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning webhook deliveries: %w", err)
	}
	return result.RowsAffected()
}
