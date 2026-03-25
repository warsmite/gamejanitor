package service

// ExportIntersectIDs exports intersectIDs for testing.
func ExportIntersectIDs(requested, allowed []string) []string {
	return intersectIDs(requested, allowed)
}
