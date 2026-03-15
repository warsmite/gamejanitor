package models

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

type Token struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	HashedToken   string          `json:"-"`
	Scope         string          `json:"scope"`
	GameserverIDs json.RawMessage `json:"gameserver_ids"`
	Permissions   json.RawMessage `json:"permissions"`
	CreatedAt     time.Time       `json:"created_at"`
	LastUsedAt    *time.Time      `json:"last_used_at,omitempty"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

func ListTokens(db *sql.DB) ([]Token, error) {
	rows, err := db.Query("SELECT id, name, hashed_token, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens ORDER BY created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close()

	var tokens []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.HashedToken, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scanning token row: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func GetToken(db *sql.DB, id string) (*Token, error) {
	var t Token
	err := db.QueryRow("SELECT id, name, hashed_token, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE id = ?", id).
		Scan(&t.ID, &t.Name, &t.HashedToken, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token %s: %w", id, err)
	}
	return &t, nil
}

func GetTokenByScope(db *sql.DB, scope string) (*Token, error) {
	var t Token
	err := db.QueryRow("SELECT id, name, hashed_token, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE scope = ? LIMIT 1", scope).
		Scan(&t.ID, &t.Name, &t.HashedToken, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token by scope %s: %w", scope, err)
	}
	return &t, nil
}

func CreateToken(db *sql.DB, t *Token) error {
	t.CreatedAt = time.Now()
	_, err := db.Exec(
		"INSERT INTO tokens (id, name, hashed_token, scope, gameserver_ids, permissions, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		t.ID, t.Name, t.HashedToken, t.Scope, t.GameserverIDs, t.Permissions, t.CreatedAt, t.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("creating token %s: %w", t.ID, err)
	}
	return nil
}

func UpdateTokenLastUsed(db *sql.DB, id string) error {
	now := time.Now()
	_, err := db.Exec("UPDATE tokens SET last_used_at = ? WHERE id = ?", now, id)
	if err != nil {
		return fmt.Errorf("updating last_used_at for token %s: %w", id, err)
	}
	return nil
}

func DeleteToken(db *sql.DB, id string) error {
	result, err := db.Exec("DELETE FROM tokens WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("deleting token %s: %w", id, err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking rows affected for token %s: %w", id, err)
	}
	if rows == 0 {
		return fmt.Errorf("token %s not found", id)
	}
	return nil
}

func DeleteTokensByScope(db *sql.DB, scope string) error {
	_, err := db.Exec("DELETE FROM tokens WHERE scope = ?", scope)
	if err != nil {
		return fmt.Errorf("deleting tokens by scope %s: %w", scope, err)
	}
	return nil
}
