package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

type TokenStore struct {
	db *sql.DB
}

func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

func (s *TokenStore) ListTokens() ([]model.Token, error) {
	return s.listTokens("SELECT id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens ORDER BY created_at DESC")
}

func (s *TokenStore) ListTokensByScope(scope string) ([]model.Token, error) {
	return s.listTokens("SELECT id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE scope = ? ORDER BY created_at DESC", scope)
}

func (s *TokenStore) listTokens(query string, args ...any) ([]model.Token, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close()

	var tokens []model.Token
	for rows.Next() {
		var t model.Token
		if err := rows.Scan(&t.ID, &t.Name, &t.HashedToken, &t.TokenPrefix, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scanning token row: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *TokenStore) GetToken(id string) (*model.Token, error) {
	var t model.Token
	err := s.db.QueryRow("SELECT id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE id = ?", id).
		Scan(&t.ID, &t.Name, &t.HashedToken, &t.TokenPrefix, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token %s: %w", id, err)
	}
	return &t, nil
}

// GetTokenByPrefix finds a token candidate by its prefix for fast lookup.
// Returns nil if no token with this prefix exists.
func (s *TokenStore) GetTokenByPrefix(prefix string) (*model.Token, error) {
	var t model.Token
	err := s.db.QueryRow("SELECT id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE token_prefix = ?", prefix).
		Scan(&t.ID, &t.Name, &t.HashedToken, &t.TokenPrefix, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token by prefix %s: %w", prefix, err)
	}
	return &t, nil
}

func (s *TokenStore) CreateToken(t *model.Token) error {
	t.CreatedAt = time.Now()
	_, err := s.db.Exec(
		"INSERT INTO tokens (id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		t.ID, t.Name, t.HashedToken, t.TokenPrefix, t.Scope, t.GameserverIDs, t.Permissions, t.CreatedAt, t.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("creating token %s: %w", t.ID, err)
	}
	return nil
}

func (s *TokenStore) UpdateTokenLastUsed(id string) error {
	now := time.Now()
	_, err := s.db.Exec("UPDATE tokens SET last_used_at = ? WHERE id = ?", now, id)
	if err != nil {
		return fmt.Errorf("updating last_used_at for token %s: %w", id, err)
	}
	return nil
}

func (s *TokenStore) DeleteToken(id string) error {
	result, err := s.db.Exec("DELETE FROM tokens WHERE id = ?", id)
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

// GetTokenByNameAndScope finds a token by its (name, scope) pair.
// Returns nil if no matching token exists.
func (s *TokenStore) GetTokenByNameAndScope(name string, scope string) (*model.Token, error) {
	var t model.Token
	err := s.db.QueryRow(
		"SELECT id, name, hashed_token, token_prefix, scope, gameserver_ids, permissions, created_at, last_used_at, expires_at FROM tokens WHERE name = ? AND scope = ?",
		name, scope,
	).Scan(&t.ID, &t.Name, &t.HashedToken, &t.TokenPrefix, &t.Scope, &t.GameserverIDs, &t.Permissions, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token by name=%s scope=%s: %w", name, scope, err)
	}
	return &t, nil
}

// DeleteTokenByNameAndScope removes a token by its (name, scope) pair.
// Returns false if no matching token existed.
func (s *TokenStore) DeleteTokenByNameAndScope(name string, scope string) (bool, error) {
	result, err := s.db.Exec("DELETE FROM tokens WHERE name = ? AND scope = ?", name, scope)
	if err != nil {
		return false, fmt.Errorf("deleting token name=%s scope=%s: %w", name, scope, err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

// TokenExistsByScope checks if a token with the given ID and scope still exists and is not expired.
func (s *TokenStore) TokenExistsByScope(id string, scope string) bool {
	var exists int
	err := s.db.QueryRow(
		"SELECT 1 FROM tokens WHERE id = ? AND scope = ? AND (expires_at IS NULL OR expires_at > ?)",
		id, scope, time.Now(),
	).Scan(&exists)
	return err == nil
}
