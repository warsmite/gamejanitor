package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/store"
	"github.com/warsmite/gamejanitor/testutil"
)

func newTestToken(id, name, role string) *model.Token {
	return &model.Token{
		ID:            id,
		Name:          name,
		HashedToken:   "hashed-" + id,
		TokenPrefix:   "pfx-" + id,
		Role:          role,
	}
}

func TestToken_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok := newTestToken("tok-1", "Admin Token", "admin")
	require.NoError(t, db.CreateToken(tok))
	assert.False(t, tok.CreatedAt.IsZero())

	got, err := db.GetToken("tok-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "tok-1", got.ID)
	assert.Equal(t, "Admin Token", got.Name)
	assert.Equal(t, "hashed-tok-1", got.HashedToken)
	assert.Equal(t, "pfx-tok-1", got.TokenPrefix)
	assert.Equal(t, "admin", got.Role)
	assert.Nil(t, got.LastUsedAt)
	assert.Nil(t, got.ExpiresAt)
}

func TestToken_GetNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	got, err := db.GetToken("nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestToken_GetByPrefix(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok := newTestToken("tok-pfx", "Prefix Token", "user")
	require.NoError(t, db.CreateToken(tok))

	got, err := db.GetTokenByPrefix("pfx-tok-pfx")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tok-pfx", got.ID)

	notFound, err := db.GetTokenByPrefix("nonexistent-prefix")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestToken_ListTokens(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok1 := newTestToken("tok-1", "First", "admin")
	tok2 := newTestToken("tok-2", "Second", "user")
	require.NoError(t, db.CreateToken(tok1))
	// Small sleep so created_at ordering is deterministic.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, db.CreateToken(tok2))

	list, err := db.ListTokens()
	require.NoError(t, err)
	assert.Len(t, list, 2)
	// ORDER BY created_at DESC — most recent first.
	assert.Equal(t, "tok-2", list[0].ID)
	assert.Equal(t, "tok-1", list[1].ID)
}

func TestToken_Delete(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok := newTestToken("tok-del", "Delete Me", "admin")
	require.NoError(t, db.CreateToken(tok))

	require.NoError(t, db.DeleteToken("tok-del"))

	got, err := db.GetToken("tok-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestToken_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	err := db.DeleteToken("nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestToken_UpdateLastUsed(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok := newTestToken("tok-used", "Used Token", "admin")
	require.NoError(t, db.CreateToken(tok))

	require.NoError(t, db.UpdateTokenLastUsed("tok-used"))

	got, err := db.GetToken("tok-used")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.LastUsedAt)
	assert.WithinDuration(t, time.Now(), *got.LastUsedAt, 5*time.Second)
}

func TestToken_WithExpiry(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	expiry := time.Now().Add(24 * time.Hour)
	tok := newTestToken("tok-exp", "Expiring Token", "user")
	tok.ExpiresAt = &expiry
	require.NoError(t, db.CreateToken(tok))

	got, err := db.GetToken("tok-exp")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.ExpiresAt)
	assert.WithinDuration(t, expiry, *got.ExpiresAt, time.Second)
}

func TestToken_QuotaFields(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	maxGS := 5
	maxMem := 4096
	tok := newTestToken("tok-quota", "Quota Token", "user")
	tok.MaxGameservers = &maxGS
	tok.MaxMemoryMB = &maxMem
	require.NoError(t, db.CreateToken(tok))

	got, err := db.GetToken("tok-quota")
	require.NoError(t, err)
	require.NotNil(t, got)

	require.NotNil(t, got.MaxGameservers)
	assert.Equal(t, 5, *got.MaxGameservers)
	require.NotNil(t, got.MaxMemoryMB)
	assert.Equal(t, 4096, *got.MaxMemoryMB)
	assert.Nil(t, got.MaxCPU)
	assert.Nil(t, got.MaxStorageMB)
}

func TestToken_ExistsByRole_ValidToken(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	tok := newTestToken("tok-exists", "Exists Token", "admin")
	require.NoError(t, db.CreateToken(tok))

	assert.True(t, db.TokenExistsByRole("tok-exists", "admin"))
	assert.False(t, db.TokenExistsByRole("tok-exists", "worker"))
	assert.False(t, db.TokenExistsByRole("nonexistent", "admin"))
}

func TestToken_ExistsByRole_ExpiredToken(t *testing.T) {
	t.Parallel()
	db := store.New(testutil.NewTestDB(t))

	expired := time.Now().Add(-1 * time.Hour)
	tok := newTestToken("tok-expired", "Expired Token", "admin")
	tok.ExpiresAt = &expired
	require.NoError(t, db.CreateToken(tok))

	assert.False(t, db.TokenExistsByRole("tok-expired", "admin"))
}
