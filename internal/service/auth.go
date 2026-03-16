package service

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/0xkowalskidev/gamejanitor/internal/models"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const (
	ScopeAdmin      = "admin"
	ScopeGameserver = "gameserver"
	ScopeWorker     = "worker"
)

// All available permissions for scoped tokens.
var AllPermissions = []string{"start", "stop", "restart", "console", "files", "backups", "settings"}

type AuthService struct {
	db  *sql.DB
	log *slog.Logger
}

func NewAuthService(db *sql.DB, log *slog.Logger) *AuthService {
	return &AuthService{db: db, log: log}
}

// ValidateToken checks a raw token string against all stored tokens.
// Returns the matching Token if valid, nil if invalid/expired.
func (s *AuthService) ValidateToken(rawToken string) *models.Token {
	tokens, err := models.ListTokens(s.db)
	if err != nil {
		s.log.Error("failed to list tokens for validation", "error", err)
		return nil
	}

	for _, t := range tokens {
		if err := bcrypt.CompareHashAndPassword([]byte(t.HashedToken), []byte(rawToken)); err != nil {
			continue
		}

		// Check expiry
		if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			s.log.Debug("token expired", "id", t.ID, "expired_at", t.ExpiresAt)
			return nil
		}

		// Update last used (best effort)
		if err := models.UpdateTokenLastUsed(s.db, t.ID); err != nil {
			s.log.Warn("failed to update token last_used_at", "id", t.ID, "error", err)
		}

		return &t
	}

	return nil
}

// GenerateAdminToken creates a new admin token, replacing any existing one.
// Returns the raw (unhashed) token string — must be shown to user once.
func (s *AuthService) GenerateAdminToken() (string, error) {
	// Delete existing admin tokens
	if err := models.DeleteTokensByScope(s.db, ScopeAdmin); err != nil {
		return "", fmt.Errorf("clearing existing admin tokens: %w", err)
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing token: %w", err)
	}

	token := &models.Token{
		ID:            uuid.New().String(),
		Name:          "Admin",
		HashedToken:   string(hashed),
		Scope:         ScopeAdmin,
		GameserverIDs: json.RawMessage("[]"),
		Permissions:   json.RawMessage("[]"),
	}

	if err := models.CreateToken(s.db, token); err != nil {
		return "", fmt.Errorf("saving admin token: %w", err)
	}

	s.log.Info("admin token generated", "id", token.ID)
	return rawToken, nil
}

// CreateScopedToken creates a new scoped token for specific gameservers with specific permissions.
// Returns the raw token string — must be shown to user once.
func (s *AuthService) CreateScopedToken(name string, gameserverIDs []string, permissions []string, expiresAt *time.Time) (string, *models.Token, error) {
	if name == "" {
		return "", nil, fmt.Errorf("token name is required")
	}

	// Validate permissions
	for _, p := range permissions {
		if !isValidPermission(p) {
			return "", nil, fmt.Errorf("invalid permission: %s", p)
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

	gsIDsJSON, _ := json.Marshal(gameserverIDs)
	permsJSON, _ := json.Marshal(permissions)

	token := &models.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		Scope:         ScopeGameserver,
		GameserverIDs: gsIDsJSON,
		Permissions:   permsJSON,
		ExpiresAt:     expiresAt,
	}

	if err := models.CreateToken(s.db, token); err != nil {
		return "", nil, fmt.Errorf("saving scoped token: %w", err)
	}

	s.log.Info("scoped token created", "id", token.ID, "name", name, "gameservers", len(gameserverIDs), "permissions", permissions)
	return rawToken, token, nil
}

// CreateWorkerToken creates a new worker token for gRPC authentication.
// Returns the raw token string — must be shown to the admin once.
func (s *AuthService) CreateWorkerToken(name string) (string, *models.Token, error) {
	if name == "" {
		return "", nil, fmt.Errorf("token name is required")
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

func (s *AuthService) GetToken(id string) (*models.Token, error) {
	return models.GetToken(s.db, id)
}

func (s *AuthService) DeleteToken(id string) error {
	s.log.Info("deleting token", "id", id)
	return models.DeleteToken(s.db, id)
}

// HasAdminToken returns true if an admin token exists in the database.
func (s *AuthService) HasAdminToken() bool {
	t, err := models.GetTokenByScope(s.db, ScopeAdmin)
	return err == nil && t != nil
}

// IsAdmin returns true if the token has admin scope.
func IsAdmin(token *models.Token) bool {
	return token != nil && token.Scope == ScopeAdmin
}

// HasPermission returns true if the token grants the given permission on the given gameserver.
func HasPermission(token *models.Token, gameserverID string, permission string) bool {
	if token == nil {
		return false
	}
	if token.Scope == ScopeAdmin {
		return true
	}

	// Check gameserver access
	var gsIDs []string
	if err := json.Unmarshal(token.GameserverIDs, &gsIDs); err != nil {
		return false
	}
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

func isValidPermission(p string) bool {
	for _, valid := range AllPermissions {
		if p == valid {
			return true
		}
	}
	return false
}

func generateSecureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gj_" + hex.EncodeToString(b), nil
}
