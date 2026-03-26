package model

import (
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type WebhookEndpoint struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Secret      string    `json:"-"`
	Events      string    `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func ListWebhookEndpoints(db *sql.DB) ([]WebhookEndpoint, error) {
	rows, err := db.Query(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []WebhookEndpoint
	for rows.Next() {
		var e WebhookEndpoint
		if err := rows.Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}

func ListEnabledWebhookEndpoints(db *sql.DB) ([]WebhookEndpoint, error) {
	rows, err := db.Query(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints WHERE enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var endpoints []WebhookEndpoint
	for rows.Next() {
		var e WebhookEndpoint
		if err := rows.Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, e)
	}
	return endpoints, rows.Err()
}

func GetWebhookEndpoint(db *sql.DB, id string) (*WebhookEndpoint, error) {
	var e WebhookEndpoint
	err := db.QueryRow(`SELECT id, description, url, secret, events, enabled, created_at, updated_at FROM webhook_endpoints WHERE id = ?`, id).
		Scan(&e.ID, &e.Description, &e.URL, &e.Secret, &e.Events, &e.Enabled, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func CreateWebhookEndpoint(db *sql.DB, e *WebhookEndpoint) error {
	e.ID = uuid.New().String()
	now := time.Now()
	e.CreatedAt = now
	e.UpdatedAt = now

	_, err := db.Exec(`INSERT INTO webhook_endpoints (id, description, url, secret, events, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Description, e.URL, e.Secret, e.Events, e.Enabled, e.CreatedAt, e.UpdatedAt)
	return err
}

func UpdateWebhookEndpoint(db *sql.DB, e *WebhookEndpoint) error {
	e.UpdatedAt = time.Now()
	res, err := db.Exec(`UPDATE webhook_endpoints SET description = ?, url = ?, secret = ?, events = ?, enabled = ?, updated_at = ? WHERE id = ?`,
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

func DeleteWebhookEndpoint(db *sql.DB, id string) error {
	res, err := db.Exec(`DELETE FROM webhook_endpoints WHERE id = ?`, id)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}
