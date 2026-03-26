package model

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	WebhookStatePending   = "pending"
	WebhookStateDelivered = "delivered"
	WebhookStateFailed    = "failed"
)

type WebhookDelivery struct {
	ID                string
	WebhookEndpointID string
	EventType         string
	Payload           string // pre-serialized JSON
	State             string
	Attempts          int
	LastAttemptAt     *time.Time
	NextAttemptAt     time.Time
	LastError         string
	CreatedAt         time.Time
}

func CreateWebhookDelivery(db *sql.DB, d *WebhookDelivery) error {
	_, err := db.Exec(
		`INSERT INTO webhook_deliveries (id, webhook_endpoint_id, event_type, payload, state, attempts, next_attempt_at, created_at)
		 VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
		d.ID, d.WebhookEndpointID, d.EventType, d.Payload, WebhookStatePending, d.NextAttemptAt, d.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("creating webhook delivery: %w", err)
	}
	return nil
}

func ListDeliveriesByEndpoint(db *sql.DB, endpointID string, state string, limit int) ([]WebhookDelivery, error) {
	query := `SELECT id, webhook_endpoint_id, event_type, payload, state, attempts, last_attempt_at, next_attempt_at, last_error, created_at
		 FROM webhook_deliveries WHERE webhook_endpoint_id = ?`
	args := []any{endpointID}

	if state != "" {
		query += ` AND state = ?`
		args = append(args, state)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying deliveries by endpoint: %w", err)
	}
	defer rows.Close()

	var deliveries []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.ID, &d.WebhookEndpointID, &d.EventType, &d.Payload, &d.State, &d.Attempts, &d.LastAttemptAt, &d.NextAttemptAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning webhook delivery row: %w", err)
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func GetPendingDeliveries(db *sql.DB, limit int) ([]WebhookDelivery, error) {
	rows, err := db.Query(
		`SELECT d.id, d.webhook_endpoint_id, d.event_type, d.payload, d.state, d.attempts, d.last_attempt_at, d.next_attempt_at, d.last_error, d.created_at
		 FROM webhook_deliveries d
		 WHERE d.state = ? AND d.next_attempt_at <= datetime('now')
		 ORDER BY d.next_attempt_at
		 LIMIT ?`,
		WebhookStatePending, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("querying pending webhook deliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(&d.ID, &d.WebhookEndpointID, &d.EventType, &d.Payload, &d.State, &d.Attempts, &d.LastAttemptAt, &d.NextAttemptAt, &d.LastError, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning webhook delivery row: %w", err)
		}
		deliveries = append(deliveries, d)
	}
	return deliveries, rows.Err()
}

func MarkDeliverySuccess(db *sql.DB, id string) error {
	_, err := db.Exec(
		`UPDATE webhook_deliveries SET state = ?, attempts = attempts + 1, last_attempt_at = datetime('now') WHERE id = ?`,
		WebhookStateDelivered, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s as delivered: %w", id, err)
	}
	return nil
}

func MarkDeliveryRetry(db *sql.DB, id string, nextAttemptAt time.Time, lastError string) error {
	_, err := db.Exec(
		`UPDATE webhook_deliveries SET attempts = attempts + 1, last_attempt_at = datetime('now'), next_attempt_at = ?, last_error = ? WHERE id = ?`,
		nextAttemptAt, lastError, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s for retry: %w", id, err)
	}
	return nil
}

func MarkDeliveryFailed(db *sql.DB, id string, lastError string) error {
	_, err := db.Exec(
		`UPDATE webhook_deliveries SET state = ?, attempts = attempts + 1, last_attempt_at = datetime('now'), last_error = ? WHERE id = ?`,
		WebhookStateFailed, lastError, id,
	)
	if err != nil {
		return fmt.Errorf("marking delivery %s as failed: %w", id, err)
	}
	return nil
}

func PruneWebhookDeliveries(db *sql.DB) (int64, error) {
	result, err := db.Exec(
		`DELETE FROM webhook_deliveries WHERE
		 (state = ? AND created_at < datetime('now', '-7 days')) OR
		 (state = ? AND created_at < datetime('now', '-30 days'))`,
		WebhookStateDelivered, WebhookStateFailed,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning webhook deliveries: %w", err)
	}
	return result.RowsAffected()
}
