package steam

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewIndividualSteamID(t *testing.T) {
	// Zero account — used for logon
	id := NewIndividualSteamID(0)
	assert.Equal(t, uint32(0), id.AccountID())
	assert.NotEqual(t, uint64(0), id.Uint64()) // should have type/universe bits set

	// Specific account
	id = NewIndividualSteamID(12345)
	assert.Equal(t, uint32(12345), id.AccountID())
}

func TestNewAnonymousSteamID(t *testing.T) {
	id := NewAnonymousSteamID()
	assert.Equal(t, uint32(0), id.AccountID())
	assert.NotEqual(t, uint64(0), id.Uint64())

	// Should be different from individual
	individual := NewIndividualSteamID(0)
	assert.NotEqual(t, individual.Uint64(), id.Uint64())
}
