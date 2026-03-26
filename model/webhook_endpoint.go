package model

import "time"

type WebhookEndpoint struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	URL         string    `json:"url"`
	Secret      string    `json:"-"`
	Events      StringSlice `json:"events"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
