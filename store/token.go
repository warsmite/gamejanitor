package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/warsmite/gamejanitor/model"
)

const tokenColumns = "id, name, hashed_token, token_prefix, role, max_gameservers, max_memory_mb, max_cpu, max_storage_mb, claim_code, created_at, last_used_at, expires_at"

type TokenStore struct {
	db *sql.DB
}

func NewTokenStore(db *sql.DB) *TokenStore {
	return &TokenStore{db: db}
}

func scanToken(scan func(dest ...any) error) (model.Token, error) {
	var t model.Token
	err := scan(&t.ID, &t.Name, &t.HashedToken, &t.TokenPrefix, &t.Role, &t.MaxGameservers, &t.MaxMemoryMB, &t.MaxCPU, &t.MaxStorageMB, &t.ClaimCode, &t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt)
	return t, err
}

func (s *TokenStore) ListTokens() ([]model.Token, error) {
	return s.listTokens("SELECT "+tokenColumns+" FROM tokens ORDER BY created_at DESC")
}

func (s *TokenStore) ListTokensByRole(role string) ([]model.Token, error) {
	return s.listTokens("SELECT "+tokenColumns+" FROM tokens WHERE role = ? ORDER BY created_at DESC", role)
}

func (s *TokenStore) listTokens(query string, args ...any) ([]model.Token, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tokens: %w", err)
	}
	defer rows.Close()

	var tokens []model.Token
	for rows.Next() {
		t, err := scanToken(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scanning token row: %w", err)
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (s *TokenStore) GetToken(id string) (*model.Token, error) {
	t, err := scanToken(s.db.QueryRow("SELECT "+tokenColumns+" FROM tokens WHERE id = ?", id).Scan)
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
	t, err := scanToken(s.db.QueryRow("SELECT "+tokenColumns+" FROM tokens WHERE token_prefix = ?", prefix).Scan)
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
		"INSERT INTO tokens (id, name, hashed_token, token_prefix, role, max_gameservers, max_memory_mb, max_cpu, max_storage_mb, claim_code, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		t.ID, t.Name, t.HashedToken, t.TokenPrefix, t.Role, t.MaxGameservers, t.MaxMemoryMB, t.MaxCPU, t.MaxStorageMB, t.ClaimCode, t.CreatedAt, t.ExpiresAt,
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

// GetTokenByNameAndRole finds a token by its (name, role) pair.
// Returns nil if no matching token exists.
func (s *TokenStore) GetTokenByNameAndRole(name string, role string) (*model.Token, error) {
	t, err := scanToken(s.db.QueryRow(
		"SELECT "+tokenColumns+" FROM tokens WHERE name = ? AND role = ?",
		name, role,
	).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token by name=%s role=%s: %w", name, role, err)
	}
	return &t, nil
}

// DeleteTokenByNameAndRole removes a token by its (name, role) pair.
// Returns false if no matching token existed.
func (s *TokenStore) DeleteTokenByNameAndRole(name string, role string) (bool, error) {
	result, err := s.db.Exec("DELETE FROM tokens WHERE name = ? AND role = ?", name, role)
	if err != nil {
		return false, fmt.Errorf("deleting token name=%s role=%s: %w", name, role, err)
	}
	rows, _ := result.RowsAffected()
	return rows > 0, nil
}

// TokenExistsByRole checks if a token with the given ID and role still exists and is not expired.
func (s *TokenStore) TokenExistsByRole(id string, role string) bool {
	var exists int
	err := s.db.QueryRow(
		"SELECT 1 FROM tokens WHERE id = ? AND role = ? AND (expires_at IS NULL OR expires_at > ?)",
		id, role, time.Now(),
	).Scan(&exists)
	return err == nil
}

// --- Claim codes ---

// GetTokenByClaimCode finds a token by its claim code.
// Returns nil if no token with this code exists.
func (s *TokenStore) GetTokenByClaimCode(code string) (*model.Token, error) {
	t, err := scanToken(s.db.QueryRow("SELECT "+tokenColumns+" FROM tokens WHERE claim_code = ?", code).Scan)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting token by claim code: %w", err)
	}
	return &t, nil
}

// SetClaimCode sets or clears a claim code on a token.
func (s *TokenStore) SetClaimCode(tokenID string, code *string) error {
	_, err := s.db.Exec("UPDATE tokens SET claim_code = ? WHERE id = ?", code, tokenID)
	if err != nil {
		return fmt.Errorf("setting claim code for token %s: %w", tokenID, err)
	}
	return nil
}

// ClearClaimCode removes the claim code from a token (after redemption).
func (s *TokenStore) ClearClaimCode(tokenID string) error {
	return s.SetClaimCode(tokenID, nil)
}

// RekeyToken updates a token's hash, prefix, and clears the claim code in one operation.
// Used when a claim code is redeemed — generates a fresh raw token for the new holder.
func (s *TokenStore) RekeyToken(tokenID, hashedToken, prefix string) error {
	_, err := s.db.Exec(
		"UPDATE tokens SET hashed_token = ?, token_prefix = ?, claim_code = NULL WHERE id = ?",
		hashedToken, prefix, tokenID,
	)
	if err != nil {
		return fmt.Errorf("re-keying token %s: %w", tokenID, err)
	}
	return nil
}
