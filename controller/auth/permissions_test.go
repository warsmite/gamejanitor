package auth_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/warsmite/gamejanitor/model"
)

func TestPermission_HasPermission_NilToken_ReturnsFalse(t *testing.T) {
	t.Parallel()
	assert.False(t, auth.HasPermission(nil, "any-id", auth.PermGameserverStart))
}

func TestPermission_HasPermission_AdminRole_AlwaysTrue(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Role:          "admin",
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice{},
	}
	assert.True(t, auth.HasPermission(token, "any-id", auth.PermGameserverStart))
	assert.True(t, auth.HasPermission(token, "other-id", auth.PermGameserverDelete))
	assert.True(t, auth.HasPermission(token, "", auth.PermSettingsEdit))
}

func TestPermission_HasPermission_UserRole_ChecksGameserverIDs(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Role:          "user",
		GameserverIDs: model.StringSlice{"gs-1", "gs-2"},
		Permissions:   model.StringSlice{"gameserver.start"},
	}
	assert.True(t, auth.HasPermission(token, "gs-1", auth.PermGameserverStart))
	assert.True(t, auth.HasPermission(token, "gs-2", auth.PermGameserverStart))
	assert.False(t, auth.HasPermission(token, "gs-3", auth.PermGameserverStart))
}

func TestPermission_HasPermission_UserRole_EmptyIDs_NoAccess(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Role:          "user",
		GameserverIDs: model.StringSlice{},
		Permissions:   model.StringSlice{"gameserver.start"},
	}
	// Empty gameserver_ids = no granted access (only owns what they created)
	assert.False(t, auth.HasPermission(token, "any-gs", auth.PermGameserverStart))
}

func TestPermission_HasPermission_UserRole_MissingPermission(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Role:          "user",
		GameserverIDs: model.StringSlice{"gs-1"},
		Permissions:   model.StringSlice{"gameserver.start"},
	}
	assert.True(t, auth.HasPermission(token, "gs-1", auth.PermGameserverStart))
	assert.False(t, auth.HasPermission(token, "gs-1", auth.PermGameserverDelete))
}

func TestPermission_IsAdmin(t *testing.T) {
	t.Parallel()
	admin := &model.Token{Role: "admin"}
	user := &model.Token{Role: "user"}
	worker := &model.Token{Role: "worker"}

	assert.True(t, auth.IsAdmin(admin))
	assert.False(t, auth.IsAdmin(user))
	assert.False(t, auth.IsAdmin(worker))
	assert.False(t, auth.IsAdmin(nil))
}
