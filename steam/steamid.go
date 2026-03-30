package steam

// SteamID encodes account type, universe, instance, and account number
// into a single uint64 per Valve's SteamID format.
type SteamID uint64

const (
	universePublic  uint64 = 1
	accountIndividual uint64 = 1
	accountAnonUser   uint64 = 10
	instanceDesktop   uint64 = 1
)

// NewIndividualSteamID builds a SteamID for an individual user account.
// If accountID is 0, returns a blank individual SteamID suitable for logon.
func NewIndividualSteamID(accountID uint32) SteamID {
	// Layout: [AccountID:32][Instance:20][AccountType:4][Universe:8]
	var id uint64
	id |= uint64(accountID)
	id |= instanceDesktop << 32
	id |= accountIndividual << 52
	id |= universePublic << 56
	return SteamID(id)
}

// NewAnonymousSteamID builds a SteamID for anonymous logon.
func NewAnonymousSteamID() SteamID {
	var id uint64
	// Instance 0, AccountType AnonUser (10), Universe Public
	id |= accountAnonUser << 52
	id |= universePublic << 56
	return SteamID(id)
}

func (s SteamID) Uint64() uint64 {
	return uint64(s)
}

func (s SteamID) AccountID() uint32 {
	return uint32(s)
}
