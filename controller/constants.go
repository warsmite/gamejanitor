package controller

// Instance contract constants.
const (
	PortNameQuery = "query" // Port used for server query polling (A2S/GJQ)
	PortNameGame  = "game"  // Fallback port for query polling if no "query" port defined
)

// Disabled capability names — used in game definitions to opt out of features.
const CapabilityQuery = "query"
