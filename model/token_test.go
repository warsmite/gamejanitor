package model_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/testutil"
)

func newTestToken(id, name, scope string) *model.Token {
	return &model.Token{
		ID:            id,
		Name:          name,
		HashedToken:   "hashed-" + id,
		TokenPrefix:   "pfx-" + id,
		Scope:         scope,
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`[]`),
	}
}

func TestToken_CreateAndGet(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-1", "Admin Token", "admin")
	require.NoError(t, model.CreateToken(db, tok))
	assert.False(t, tok.CreatedAt.IsZero())

	got, err := model.GetToken(db, "tok-1")
	require.NoError(t, err)
	require.NotNil(t, got)

	assert.Equal(t, "tok-1", got.ID)
	assert.Equal(t, "Admin Token", got.Name)
	assert.Equal(t, "hashed-tok-1", got.HashedToken)
	assert.Equal(t, "pfx-tok-1", got.TokenPrefix)
	assert.Equal(t, "admin", got.Scope)
	assert.Nil(t, got.LastUsedAt)
	assert.Nil(t, got.ExpiresAt)
}

func TestToken_GetNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	got, err := model.GetToken(db, "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestToken_GetByPrefix(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-pfx", "Prefix Token", "gameserver")
	require.NoError(t, model.CreateToken(db, tok))

	got, err := model.GetTokenByPrefix(db, "pfx-tok-pfx")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "tok-pfx", got.ID)

	notFound, err := model.GetTokenByPrefix(db, "nonexistent-prefix")
	require.NoError(t, err)
	assert.Nil(t, notFound)
}

func TestToken_ListTokens(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok1 := newTestToken("tok-1", "First", "admin")
	tok2 := newTestToken("tok-2", "Second", "gameserver")
	require.NoError(t, model.CreateToken(db, tok1))
	// Small sleep so created_at ordering is deterministic.
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, model.CreateToken(db, tok2))

	list, err := model.ListTokens(db)
	require.NoError(t, err)
	assert.Len(t, list, 2)
	// ORDER BY created_at DESC — most recent first.
	assert.Equal(t, "tok-2", list[0].ID)
	assert.Equal(t, "tok-1", list[1].ID)
}

func TestToken_Delete(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-del", "Delete Me", "admin")
	require.NoError(t, model.CreateToken(db, tok))

	require.NoError(t, model.DeleteToken(db, "tok-del"))

	got, err := model.GetToken(db, "tok-del")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestToken_DeleteNotFound(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	err := model.DeleteToken(db, "nonexistent")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestToken_UpdateLastUsed(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-used", "Used Token", "admin")
	require.NoError(t, model.CreateToken(db, tok))

	require.NoError(t, model.UpdateTokenLastUsed(db, "tok-used"))

	got, err := model.GetToken(db, "tok-used")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.LastUsedAt)
	assert.WithinDuration(t, time.Now(), *got.LastUsedAt, 5*time.Second)
}

func TestToken_WithExpiry(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	expiry := time.Now().Add(24 * time.Hour)
	tok := newTestToken("tok-exp", "Expiring Token", "gameserver")
	tok.ExpiresAt = &expiry
	require.NoError(t, model.CreateToken(db, tok))

	got, err := model.GetToken(db, "tok-exp")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.ExpiresAt)
	assert.WithinDuration(t, expiry, *got.ExpiresAt, time.Second)
}

func TestToken_GameserverIDsJSON(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-gs", "Scoped Token", "gameserver")
	tok.GameserverIDs = json.RawMessage(`["gs-1","gs-2"]`)
	tok.Permissions = json.RawMessage(`["console","backup.create"]`)
	require.NoError(t, model.CreateToken(db, tok))

	got, err := model.GetToken(db, "tok-gs")
	require.NoError(t, err)
	require.NotNil(t, got)

	var gsIDs []string
	require.NoError(t, json.Unmarshal(got.GameserverIDs, &gsIDs))
	assert.Equal(t, []string{"gs-1", "gs-2"}, gsIDs)

	var perms []string
	require.NoError(t, json.Unmarshal(got.Permissions, &perms))
	assert.Equal(t, []string{"console", "backup.create"}, perms)
}

func TestToken_ExistsByScope_ValidToken(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	tok := newTestToken("tok-exists", "Exists Token", "admin")
	require.NoError(t, model.CreateToken(db, tok))

	assert.True(t, model.TokenExistsByScope(db, "tok-exists", "admin"))
	assert.False(t, model.TokenExistsByScope(db, "tok-exists", "worker"))
	assert.False(t, model.TokenExistsByScope(db, "nonexistent", "admin"))
}

func TestToken_ExistsByScope_ExpiredToken(t *testing.T) {
	t.Parallel()
	db := testutil.NewTestDB(t)

	expired := time.Now().Add(-1 * time.Hour)
	tok := newTestToken("tok-expired", "Expired Token", "admin")
	tok.ExpiresAt = &expired
	require.NoError(t, model.CreateToken(db, tok))

	assert.False(t, model.TokenExistsByScope(db, "tok-expired", "admin"))
}
