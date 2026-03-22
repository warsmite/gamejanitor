package models

import "fmt"

// Pagination adds optional limit/offset to list queries.
// Limit=0 means no limit (return all). MaxLimit caps the value if set.
type Pagination struct {
	Limit  int
	Offset int
}

// ApplyToQuery appends LIMIT/OFFSET clauses if Limit > 0.
// maxLimit caps the limit to prevent excessive responses (0 = no cap).
func (p Pagination) ApplyToQuery(query string, maxLimit int) string {
	if p.Limit <= 0 {
		return query
	}
	if maxLimit > 0 && p.Limit > maxLimit {
		p.Limit = maxLimit
	}
	query += fmt.Sprintf(" LIMIT %d", p.Limit)
	if p.Offset > 0 {
		query += fmt.Sprintf(" OFFSET %d", p.Offset)
	}
	return query
}
