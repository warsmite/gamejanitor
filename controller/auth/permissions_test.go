package auth_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/warsmite/gamejanitor/model"
)

func TestPermission_HasPermission_NilToken_ReturnsFalse(t *testing.T) {
	t.Parallel()
	assert.False(t, auth.HasPermission(nil, "any-id", auth.PermGameserverStart))
}

func TestPermission_HasPermission_AdminScope_AlwaysTrue(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "admin",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`[]`),
	}
	assert.True(t, auth.HasPermission(token, "any-id", auth.PermGameserverStart))
	assert.True(t, auth.HasPermission(token, "other-id", auth.PermGameserverDelete))
	assert.True(t, auth.HasPermission(token, "", auth.PermSettingsEdit))
}

func TestPermission_HasPermission_CustomScope_ChecksGameserverIDs(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`["gs-1","gs-2"]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, auth.HasPermission(token, "gs-1", auth.PermGameserverStart))
	assert.True(t, auth.HasPermission(token, "gs-2", auth.PermGameserverStart))
	assert.False(t, auth.HasPermission(token, "gs-3", auth.PermGameserverStart))
}

func TestPermission_HasPermission_CustomScope_EmptyIDs_AllAccess(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, auth.HasPermission(token, "any-gs", auth.PermGameserverStart))
}

func TestPermission_HasPermission_CustomScope_MissingPermission(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, auth.HasPermission(token, "gs-1", auth.PermGameserverStart))
	assert.False(t, auth.HasPermission(token, "gs-1", auth.PermGameserverDelete))
}

func TestPermission_IsAdmin(t *testing.T) {
	t.Parallel()
	admin := &model.Token{Scope: "admin"}
	custom := &model.Token{Scope: "custom"}
	worker := &model.Token{Scope: "worker"}

	assert.True(t, auth.IsAdmin(admin))
	assert.False(t, auth.IsAdmin(custom))
	assert.False(t, auth.IsAdmin(worker))
	assert.False(t, auth.IsAdmin(nil))
}
