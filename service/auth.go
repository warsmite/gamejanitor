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

	"github.com/warsmite/gamejanitor/model"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

type authContextKey string

const tokenContextKey authContextKey = "auth_token"

func SetTokenInContext(ctx context.Context, token *model.Token) context.Context {
	return context.WithValue(ctx, tokenContextKey, token)
}

func TokenFromContext(ctx context.Context) *model.Token {
	t, _ := ctx.Value(tokenContextKey).(*model.Token)
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

func (s *AuthService) ValidateToken(rawToken string) *model.Token {
	prefix := tokenPrefix(rawToken)

	// Fast path: lookup by prefix (single DB query + one bcrypt verify)
	if prefix != "" {
		t, err := model.GetTokenByPrefix(s.db, prefix)
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
				if err := model.UpdateTokenLastUsed(s.db, t.ID); err != nil {
					s.log.Warn("failed to update token last_used_at", "id", t.ID, "error", err)
				}
				return t
			}
		}
	}

	// Fallback: scan all tokens (handles tokens created before prefix was stored)
	tokens, err := model.ListTokens(s.db)
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

		if err := model.UpdateTokenLastUsed(s.db, t.ID); err != nil {
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

// Used by the Enable Auth flow. Rotates the "Admin" token so a fresh raw token
// is always returned, even if auth was previously enabled.
func (s *AuthService) GenerateAdminToken() (string, error) {
	rawToken, _, err := s.RotateAdminToken("Admin")
	if err != nil {
		return "", err
	}
	return rawToken, nil
}

func (s *AuthService) CreateAdminToken(name string) (string, *model.Token, error) {
	t := &model.Token{Name: name}
	if err := t.Validate(); err != nil {
		return "", nil, err
	}

	existing, err := model.GetTokenByNameAndScope(s.db, name, ScopeAdmin)
	if err != nil {
		return "", nil, fmt.Errorf("checking existing admin token: %w", err)
	}
	if existing != nil {
		s.log.Info("admin token already exists", "id", existing.ID, "name", name)
		return "", existing, nil
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

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeAdmin,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   permsJSON,
	}

	if err := model.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving admin token: %w", err)
	}

	s.log.Info("admin token created", "id", token.ID, "name", name)
	return rawToken, token, nil
}

func (s *AuthService) CreateCustomToken(name string, gameserverIDs []string, permissions []string, expiresAt *time.Time) (string, *model.Token, error) {
	t := &model.Token{Name: name}
	if err := t.Validate(); err != nil {
		return "", nil, err
	}

	for _, gsID := range gameserverIDs {
		gs, err := model.GetGameserver(s.db, gsID)
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

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeCustom,
		GameserverIDs: gsIDsJSON,
		Permissions:   permsJSON,
		ExpiresAt:     expiresAt,
	}

	if err := model.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving custom token: %w", err)
	}

	s.log.Info("custom token created", "id", token.ID, "name", name, "gameservers", len(gameserverIDs), "permissions", permissions)
	return rawToken, token, nil
}

func (s *AuthService) CreateWorkerToken(name string) (string, *model.Token, error) {
	if name == "" {
		return "", nil, ErrBadRequest("token name is required")
	}

	existing, err := model.GetTokenByNameAndScope(s.db, name, ScopeWorker)
	if err != nil {
		return "", nil, fmt.Errorf("checking existing worker token: %w", err)
	}
	if existing != nil {
		s.log.Info("worker token already exists", "id", existing.ID, "name", name)
		return "", existing, nil
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeWorker,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   json.RawMessage("[]"),
	}

	if err := model.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving worker token: %w", err)
	}

	s.log.Info("worker token created", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// RotateAdminToken deletes any existing admin token with the given name and creates a new one.
// Always returns a raw token. Used by GenerateAdminToken and explicit rotation.
func (s *AuthService) RotateAdminToken(name string) (string, *model.Token, error) {
	t := &model.Token{Name: name}
	if err := t.Validate(); err != nil {
		return "", nil, err
	}

	if deleted, err := model.DeleteTokenByNameAndScope(s.db, name, ScopeAdmin); err != nil {
		return "", nil, fmt.Errorf("deleting old admin token: %w", err)
	} else if deleted {
		s.log.Info("rotated out old admin token", "name", name)
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	permsJSON, _ := json.Marshal(AllPermissions)

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeAdmin,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   permsJSON,
	}

	if err := model.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving admin token: %w", err)
	}

	s.log.Info("admin token rotated", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// RotateWorkerToken deletes any existing worker token with the given name and creates a new one.
// Always returns a raw token. Used by _local worker and explicit rotation.
func (s *AuthService) RotateWorkerToken(name string) (string, *model.Token, error) {
	if name == "" {
		return "", nil, ErrBadRequest("token name is required")
	}

	if deleted, err := model.DeleteTokenByNameAndScope(s.db, name, ScopeWorker); err != nil {
		return "", nil, fmt.Errorf("deleting old worker token: %w", err)
	} else if deleted {
		s.log.Info("rotated out old worker token", "name", name)
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hashing token: %w", err)
	}

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeWorker,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   json.RawMessage("[]"),
	}

	if err := model.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving worker token: %w", err)
	}

	s.log.Info("worker token rotated", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// IsWorkerTokenValid checks if a token ID still exists with worker scope.
// Used for heartbeat fast-path validation (no bcrypt needed).
func (s *AuthService) IsWorkerTokenValid(tokenID string) bool {
	return model.TokenExistsByScope(s.db, tokenID, ScopeWorker)
}

func (s *AuthService) ListTokens() ([]model.Token, error) {
	return model.ListTokens(s.db)
}

func (s *AuthService) ListTokensByScope(scope string) ([]model.Token, error) {
	return model.ListTokensByScope(s.db, scope)
}

func (s *AuthService) GetToken(id string) (*model.Token, error) {
	return model.GetToken(s.db, id)
}

func (s *AuthService) DeleteToken(id string) error {
	s.log.Info("deleting token", "id", id)
	return model.DeleteToken(s.db, id)
}

// IsAdmin checks if the token was created as an admin token.
// The scope is a creation-time label — admin tokens have all permissions.
func IsAdmin(token *model.Token) bool {
	return token != nil && token.Scope == ScopeAdmin
}

// HasPermission checks if a token has a specific permission on a gameserver.
// Empty gameserver_ids means all-access (no ID filtering).
// Admin tokens always have permission (via scope shortcut).
func HasPermission(token *model.Token, gameserverID string, permission string) bool {
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
func AllowedGameserverIDs(token *model.Token) []string {
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
