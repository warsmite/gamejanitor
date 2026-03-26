package service_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/warsmite/gamejanitor/model"
	"github.com/warsmite/gamejanitor/service"
)

func TestPermission_HasPermission_NilToken_ReturnsFalse(t *testing.T) {
	t.Parallel()
	assert.False(t, service.HasPermission(nil, "any-id", service.PermGameserverStart))
}

func TestPermission_HasPermission_AdminScope_AlwaysTrue(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "admin",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`[]`),
	}
	assert.True(t, service.HasPermission(token, "any-id", service.PermGameserverStart))
	assert.True(t, service.HasPermission(token, "other-id", service.PermGameserverDelete))
	assert.True(t, service.HasPermission(token, "", service.PermSettingsEdit))
}

func TestPermission_HasPermission_CustomScope_ChecksGameserverIDs(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`["gs-1","gs-2"]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, service.HasPermission(token, "gs-1", service.PermGameserverStart))
	assert.True(t, service.HasPermission(token, "gs-2", service.PermGameserverStart))
	assert.False(t, service.HasPermission(token, "gs-3", service.PermGameserverStart))
}

func TestPermission_HasPermission_CustomScope_EmptyIDs_AllAccess(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, service.HasPermission(token, "any-gs", service.PermGameserverStart))
}

func TestPermission_HasPermission_CustomScope_MissingPermission(t *testing.T) {
	t.Parallel()
	token := &model.Token{
		Scope:         "custom",
		GameserverIDs: json.RawMessage(`[]`),
		Permissions:   json.RawMessage(`["gameserver.start"]`),
	}
	assert.True(t, service.HasPermission(token, "gs-1", service.PermGameserverStart))
	assert.False(t, service.HasPermission(token, "gs-1", service.PermGameserverDelete))
}

func TestPermission_IsAdmin(t *testing.T) {
	t.Parallel()
	admin := &model.Token{Scope: "admin"}
	custom := &model.Token{Scope: "custom"}
	worker := &model.Token{Scope: "worker"}

	assert.True(t, service.IsAdmin(admin))
	assert.False(t, service.IsAdmin(custom))
	assert.False(t, service.IsAdmin(worker))
	assert.False(t, service.IsAdmin(nil))
}
