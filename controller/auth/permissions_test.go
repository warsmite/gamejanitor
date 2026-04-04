package auth_test

import (
	"github.com/warsmite/gamejanitor/controller/auth"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/warsmite/gamejanitor/model"
)

func TestPermission_HasGrantPermission_EmptyGrant_AllPermissions(t *testing.T) {
	t.Parallel()
	// Empty grant list = all permissions on this server
	assert.True(t, auth.HasGrantPermission([]string{}, auth.PermGameserverStart))
	assert.True(t, auth.HasGrantPermission([]string{}, auth.PermGameserverDelete))
	assert.True(t, auth.HasGrantPermission([]string{}, auth.PermBackupCreate))
}

func TestPermission_HasGrantPermission_SpecificPerms(t *testing.T) {
	t.Parallel()
	perms := []string{auth.PermGameserverStart, auth.PermGameserverStop}
	assert.True(t, auth.HasGrantPermission(perms, auth.PermGameserverStart))
	assert.True(t, auth.HasGrantPermission(perms, auth.PermGameserverStop))
	assert.False(t, auth.HasGrantPermission(perms, auth.PermGameserverDelete))
	assert.False(t, auth.HasGrantPermission(perms, auth.PermBackupCreate))
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
