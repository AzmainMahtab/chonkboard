// Package idgenerator produces the public identifiers used across the schema.
package idgenerator

import (
	"fmt"

	"github.com/google/uuid"
)

// NewUUIDv7 returns a time-ordered UUID as its canonical lower-case string.
//
// Version 7 rather than 4 because these values are the actual primary keys.
// A v4 key scatters inserts across the whole B-tree, which turns every insert
// into a random page write and bloats the index; v7 leads with a millisecond
// timestamp, so successive inserts land next to each other. It also means
// ORDER BY uuid is roughly creation order, which is occasionally useful and
// never misleading.
//
// It panics if the system entropy source fails. There is no sensible way to
// continue without one, and returning an error here would put an err check on
// every call site that constructs a row.
func NewUUIDv7() string {
	id, err := uuid.NewV7()
	if err != nil {
		panic(fmt.Sprintf("idgenerator: no entropy available: %v", err))
	}
	return id.String()
}

// Valid reports whether s is a well-formed UUID. Route parameters are checked
// with it before they reach a query, so a malformed path segment is a 400 rather
// than a pointless round trip to the database.
func Valid(s string) bool {
	parsed, err := uuid.Parse(s)
	if err != nil {
		return false
	}
	// uuid.Parse accepts braced and urn: forms; the canonical string is the
	// only shape we ever store or emit.
	return parsed.String() == s
}
