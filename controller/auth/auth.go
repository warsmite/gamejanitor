package auth

import (
	"github.com/warsmite/gamejanitor/pkg/validate"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/warsmite/gamejanitor/model"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

// Store defines the token persistence methods auth needs.
type Store interface {
	GetTokenByPrefix(prefix string) (*model.Token, error)
	ListTokens() ([]model.Token, error)
	UpdateTokenLastUsed(id string) error
	CreateToken(t *model.Token) error
	DeleteToken(id string) error
	GetToken(id string) (*model.Token, error)
	ListTokensByScope(scope string) ([]model.Token, error)
	GetTokenByNameAndScope(name, scope string) (*model.Token, error)
	DeleteTokenByNameAndScope(name, scope string) (bool, error)
	TokenExistsByScope(id, scope string) bool
	GetGameserver(id string) (*model.Gameserver, error)
	GetTokenByClaimCode(code string) (*model.Token, error)
	SetClaimCode(tokenID string, code *string) error
	ClearClaimCode(tokenID string) error
	RekeyToken(tokenID, hashedToken, prefix string) error
}

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
	store Store
	log   *slog.Logger

	// Batched last_used_at updates — avoids a DB write on every request.
	// The map collects token IDs touched since the last flush. A background
	// goroutine writes them to the DB every few seconds.
	pendingMu sync.Mutex
	pending   map[string]struct{}
	stop      chan struct{}
	done      chan struct{}
}

const lastUsedFlushInterval = 5 * time.Second

func NewAuthService(store Store, log *slog.Logger) *AuthService {
	s := &AuthService{
		store:   store,
		log:     log,
		pending: make(map[string]struct{}),
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
	go s.flushLoop()
	return s
}

// Stop flushes any pending last_used_at updates and stops the background goroutine.
func (s *AuthService) Stop() {
	close(s.stop)
	<-s.done
}

func (s *AuthService) flushLoop() {
	defer close(s.done)
	ticker := time.NewTicker(lastUsedFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stop:
			s.flushLastUsed()
			return
		case <-ticker.C:
			s.flushLastUsed()
		}
	}
}

func (s *AuthService) flushLastUsed() {
	s.pendingMu.Lock()
	if len(s.pending) == 0 {
		s.pendingMu.Unlock()
		return
	}
	batch := s.pending
	s.pending = make(map[string]struct{})
	s.pendingMu.Unlock()

	for id := range batch {
		if err := s.store.UpdateTokenLastUsed(id); err != nil {
			s.log.Warn("failed to flush token last_used_at", "id", id, "error", err)
		}
	}
}

func (s *AuthService) touchLastUsed(tokenID string) {
	s.pendingMu.Lock()
	s.pending[tokenID] = struct{}{}
	s.pendingMu.Unlock()
}

func (s *AuthService) ValidateToken(rawToken string) *model.Token {
	prefix := tokenPrefix(rawToken)

	// Fast path: lookup by prefix (single DB query + one bcrypt verify)
	if prefix != "" {
		t, err := s.store.GetTokenByPrefix(prefix)
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
				s.touchLastUsed(t.ID)
				return t
			}
		}
	}

	// Fallback: scan all tokens (handles tokens created before prefix was stored)
	tokens, err := s.store.ListTokens()
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

		s.touchLastUsed(t.ID)
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

	existing, err := s.store.GetTokenByNameAndScope(name, ScopeAdmin)
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
	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeAdmin,
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice(AllPermissions),
	}

	if err := s.store.CreateToken(token); err != nil {
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
		gs, err := s.store.GetGameserver(gsID)
		if err != nil {
			return "", nil, fmt.Errorf("validating gameserver ID %s: %w", gsID, err)
		}
		if gs == nil {
			return "", nil, fmt.Errorf("gameserver %s not found", gsID)
		}
	}

	for _, p := range permissions {
		if !isValidPermission(p) {
			var fe validate.FieldErrors
			fe.Add("permissions", fmt.Sprintf("invalid permission: %q", p))
			return "", nil, fe
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

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeCustom,
		GameserverIDs: model.StringSlice(gameserverIDs),
		Permissions:   model.StringSlice(permissions),
		ExpiresAt:     expiresAt,
	}

	if err := s.store.CreateToken(token); err != nil {
		return "", nil, fmt.Errorf("saving custom token: %w", err)
	}

	s.log.Info("custom token created", "id", token.ID, "name", name, "gameservers", len(gameserverIDs), "permissions", permissions)
	return rawToken, token, nil
}

func (s *AuthService) CreateWorkerToken(name string) (string, *model.Token, error) {
	if name == "" {
		return "", nil, fmt.Errorf("token name is required")
	}

	existing, err := s.store.GetTokenByNameAndScope(name, ScopeWorker)
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
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice{},
	}

	if err := s.store.CreateToken(token); err != nil {
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

	if deleted, err := s.store.DeleteTokenByNameAndScope(name, ScopeAdmin); err != nil {
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

	token := &model.Token{
		ID:            uuid.New().String(),
		Name:          name,
		HashedToken:   string(hashed),
		TokenPrefix:   tokenPrefix(rawToken),
		Scope:         ScopeAdmin,
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice(AllPermissions),
	}

	if err := s.store.CreateToken(token); err != nil {
		return "", nil, fmt.Errorf("saving admin token: %w", err)
	}

	s.log.Info("admin token rotated", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// RotateWorkerToken deletes any existing worker token with the given name and creates a new one.
// Always returns a raw token. Used by _local worker and explicit rotation.
func (s *AuthService) RotateWorkerToken(name string) (string, *model.Token, error) {
	if name == "" {
		return "", nil, fmt.Errorf("token name is required")
	}

	if deleted, err := s.store.DeleteTokenByNameAndScope(name, ScopeWorker); err != nil {
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
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice{},
	}

	if err := s.store.CreateToken(token); err != nil {
		return "", nil, fmt.Errorf("saving worker token: %w", err)
	}

	s.log.Info("worker token rotated", "id", token.ID, "name", name)
	return rawToken, token, nil
}

// IsWorkerTokenValid checks if a token ID still exists with worker scope.
// Used for heartbeat fast-path validation (no bcrypt needed).
func (s *AuthService) IsWorkerTokenValid(tokenID string) bool {
	return s.store.TokenExistsByScope(tokenID, ScopeWorker)
}

func (s *AuthService) ListTokens() ([]model.Token, error) {
	return s.store.ListTokens()
}

func (s *AuthService) ListTokensByScope(scope string) ([]model.Token, error) {
	return s.store.ListTokensByScope(scope)
}

func (s *AuthService) GetToken(id string) (*model.Token, error) {
	return s.store.GetToken(id)
}

func (s *AuthService) DeleteToken(id string) error {
	s.log.Info("deleting token", "id", id)
	return s.store.DeleteToken(id)
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
	if len(token.GameserverIDs) > 0 {
		hasAccess := false
		for _, id := range token.GameserverIDs {
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
	for _, p := range token.Permissions {
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
	if len(token.GameserverIDs) == 0 {
		return nil // all-access
	}
	return []string(token.GameserverIDs)
}

// intersectIDs returns the intersection of requested and allowed ID sets.
// nil allowed means all-access (returns requested as-is).
// nil requested means no filter (returns allowed as-is).
func IntersectIDs(requested, allowed []string) []string {
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

func generateClaimCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "inv_" + hex.EncodeToString(b), nil
}

// GenerateClaimCode creates or regenerates a claim code for an existing token.
// Returns the new claim code. The old code (if any) stops working immediately.
func (s *AuthService) GenerateClaimCode(tokenID string) (string, error) {
	t, err := s.store.GetToken(tokenID)
	if err != nil {
		return "", fmt.Errorf("getting token: %w", err)
	}
	if t == nil {
		return "", fmt.Errorf("token not found")
	}

	code, err := generateClaimCode()
	if err != nil {
		return "", fmt.Errorf("generating claim code: %w", err)
	}

	if err := s.store.SetClaimCode(tokenID, &code); err != nil {
		return "", err
	}

	s.log.Info("claim code generated", "token", tokenID)
	return code, nil
}

// RedeemClaimCode exchanges a claim code for a raw token.
// Generates a fresh token (re-keys the record) so the claim code holder gets
// their own credential. The claim code is consumed — single use.
func (s *AuthService) RedeemClaimCode(code string) (string, error) {
	t, err := s.store.GetTokenByClaimCode(code)
	if err != nil {
		return "", fmt.Errorf("looking up claim code: %w", err)
	}
	if t == nil {
		return "", fmt.Errorf("invalid or expired claim code")
	}

	rawToken, err := generateSecureToken()
	if err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(rawToken), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing token: %w", err)
	}

	if err := s.store.RekeyToken(t.ID, string(hashed), tokenPrefix(rawToken)); err != nil {
		return "", err
	}

	s.log.Info("claim code redeemed", "token", t.ID, "name", t.Name)
	return rawToken, nil
}
