package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/warsmite/gamejanitor/models"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type authContextKey string

const tokenContextKey authContextKey = "auth_token"

func SetTokenInContext(ctx context.Context, token *models.Token) context.Context {
	return context.WithValue(ctx, tokenContextKey, token)
}

func TokenFromContext(ctx context.Context) *models.Token {
	t, _ := ctx.Value(tokenContextKey).(*models.Token)
	return t
}

const (
	ScopeAdmin  = "admin"
	ScopeCustom = "custom"
	ScopeWorker = "worker"
)


type AuthService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewAuthService(db *sql.DB, log *slog.Logger) *AuthService {
	return &AuthService{db: db, log: log}
}

func (s *AuthService) ValidateToken(rawToken string) *models.Token {
	prefix := tokenPrefix(rawToken)

	// Fast path: lookup by prefix (single DB query + one bcrypt verify)
	if prefix != "" {
		t, err := models.GetTokenByPrefix(s.db, prefix)
		if err != nil {
			s.log.Error("failed to lookup token by prefix", "error", err)
			return nil
		}
		if t != nil {
			if err := bcrypt.CompareHashAndPassword([]byte(t.HashedToken), []byte(rawToken)); err == nil {
				if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
					s.log.Debug("token expired", "id", t.ID, "expired_at", t.ExpiresAt)
					return nil
				}
				if err := models.UpdateTokenLastUsed(s.db, t.ID); err != nil {
					s.log.Warn("failed to update token last_used_at", "id", t.ID, "error", err)
				}
				return t
			}
		}
	}

	// Fallback: scan all tokens (handles tokens created before prefix was stored)
	tokens, err := models.ListTokens(s.db)
	if err != nil {
		s.log.Error("failed to list tokens for validation", "error", err)
		return nil
	}

	for _, t := range tokens {
		if err := bcrypt.CompareHashAndPassword([]byte(t.HashedToken), []byte(rawToken)); err != nil {
			continue
		}

		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			s.log.Debug("token expired", "id", t.ID, "expired_at", t.ExpiresAt)
			return nil
		}

		if err := models.UpdateTokenLastUsed(s.db, t.ID); err != nil {
			s.log.Warn("failed to update token last_used_at", "id", t.ID, "error", err)
		}

		return &t
	}

	return nil
}

// tokenPrefix extracts a lookup prefix from a raw token.
// Tokens are formatted as "gj_<hex>", prefix is first 16 chars after "gj_".
func tokenPrefix(rawToken string) string {
	const prefixStart = 3  // skip "gj_"
	const prefixLen = 16
	if len(rawToken) >= prefixStart+prefixLen {
		return rawToken[prefixStart : prefixStart+prefixLen]
	}
	return ""
}

// Used by the Enable Auth flow for first-time setup.
func (s *AuthService) GenerateAdminToken() (string, error) {
	rawToken, _, err := s.CreateAdminToken("Admin")
	if err != nil {
		return "", err
	}
	return rawToken, nil
}

func (s *AuthService) CreateAdminToken(name string) (string, *models.Token, error) {
	t := &models.Token{Name: name}
	if err := t.Validate(); err != nil {
		return "", nil, err
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	// Admin tokens store all permissions for consistency
	permsJSON, _ := json.Marshal(AllPermissions)

	token := &models.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeAdmin,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   permsJSON,
	}

	if err := models.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving admin token: %w", err)
	}

	s.log.Info("admin token created", "id", token.ID, "name", name)
	return rawToken, token, nil
}

func (s *AuthService) CreateCustomToken(name string, gameserverIDs []string, permissions []string, expiresAt *time.Time) (string, *models.Token, error) {
	t := &models.Token{Name: name}
	if err := t.Validate(); err != nil {
		return "", nil, err
	}

	for _, gsID := range gameserverIDs {
		gs, err := models.GetGameserver(s.db, gsID)
		if err != nil {
			return "", nil, fmt.Errorf("validating gameserver ID %s: %w", gsID, err)
		}
		if gs == nil {
			return "", nil, ErrBadRequestf("gameserver %s not found", gsID)
		}
	}

	for _, p := range permissions {
		if !isValidPermission(p) {
			return "", nil, ErrBadRequestf("invalid permission: %s", p)
		}
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	// Default to all-access if no gameserver IDs specified
	if gameserverIDs == nil {
		gameserverIDs = []string{}
	}
	gsIDsJSON, _ := json.Marshal(gameserverIDs)
	permsJSON, _ := json.Marshal(permissions)

	token := &models.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeCustom,
		GameserverIDs: gsIDsJSON,
		Permissions:   permsJSON,
		ExpiresAt:     expiresAt,
	}

	if err := models.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving custom token: %w", err)
	}

	s.log.Info("custom token created", "id", token.ID, "name", name, "gameservers", len(gameserverIDs), "permissions", permissions)
	return rawToken, token, nil
}

func (s *AuthService) CreateWorkerToken(name string) (string, *models.Token, error) {
	if name == "" {
		return "", nil, ErrBadRequest("token name is required")
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	token := &models.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeWorker,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   json.RawMessage("[]"),
	}

	if err := models.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving worker token: %w", err)
	}

	s.log.Info("worker token created", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// IsWorkerTokenValid checks if a token ID still exists with worker scope.
// Used for heartbeat fast-path validation (no bcrypt needed).
func (s *AuthService) IsWorkerTokenValid(tokenID string) bool {
	return models.TokenExistsByScope(s.db, tokenID, ScopeWorker)
}

func (s *AuthService) ListTokens() ([]models.Token, error) {
	return models.ListTokens(s.db)
}

func (s *AuthService) ListTokensByScope(scope string) ([]models.Token, error) {
	return models.ListTokensByScope(s.db, scope)
}

func (s *AuthService) GetToken(id string) (*models.Token, error) {
	return models.GetToken(s.db, id)
}

func (s *AuthService) DeleteToken(id string) error {
	s.log.Info("deleting token", "id", id)
	return models.DeleteToken(s.db, id)
}

// IsAdmin checks if the token was created as an admin token.
// The scope is a creation-time label — admin tokens have all permissions.
func IsAdmin(token *models.Token) bool {
	return token != nil && token.Scope == ScopeAdmin
}

// HasPermission checks if a token has a specific permission on a gameserver.
// Empty gameserver_ids means all-access (no ID filtering).
// Admin tokens always have permission (via scope shortcut).
func HasPermission(token *models.Token, gameserverID string, permission string) bool {
	if token == nil {
		return false
	}
	if token.Scope == ScopeAdmin {
		return true
	}

	// Check gameserver access — empty list means all-access
	var gsIDs []string
	if err := json.Unmarshal(token.GameserverIDs, &gsIDs); err != nil {
		return false
	}
	if len(gsIDs) > 0 {
		hasAccess := false
		for _, id := range gsIDs {
			if id == gameserverID {
				hasAccess = true
				break
			}
		}
		if !hasAccess {
			return false
		}
	}

	// Check permission
	var perms []string
	if err := json.Unmarshal(token.Permissions, &perms); err != nil {
		return false
	}
	for _, p := range perms {
		if p == permission {
			return true
		}
	}
	return false
}

// AllowedGameserverIDs extracts the gameserver IDs a token is scoped to.
// Returns nil if the token is nil, admin, or all-access (empty list).
func AllowedGameserverIDs(token *models.Token) []string {
	if token == nil || token.Scope == ScopeAdmin {
		return nil
	}
	var ids []string
	if err := json.Unmarshal(token.GameserverIDs, &ids); err != nil {
		return nil
	}
	if len(ids) == 0 {
		return nil // all-access
	}
	return ids
}

// intersectIDs returns the intersection of requested and allowed ID sets.
// nil allowed means all-access (returns requested as-is).
// nil requested means no filter (returns allowed as-is).
func intersectIDs(requested, allowed []string) []string {
	if len(allowed) == 0 {
		return requested
	}
	if len(requested) == 0 {
		return allowed
	}
	set := make(map[string]bool, len(allowed))
	for _, id := range allowed {
		set[id] = true
	}
	result := []string{} // empty slice, not nil — nil means "no filter"
	for _, id := range requested {
		if set[id] {
			result = append(result, id)
		}
	}
	return result
}

func generateSecureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gj_" + hex.EncodeToString(b), nil
}
