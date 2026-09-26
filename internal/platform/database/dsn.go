package database

import (
	"fmt"
	"net/url"
	"strings"
)

// DriverName is the database/sql driver this build registers. It is the one
// place the dialect is named; sqlx keys its placeholder rewriting off it.
const DriverName = "sqlite"

// pragmas are applied per connection through the DSN, because SQLite scopes them
// to a connection rather than to the database file. Setting them with an Exec
// after Open would configure exactly one connection out of the pool.
//
//	journal_mode(WAL)     readers do not block the writer, which matters because
//	                      an open SSE stream holds a connection for as long as a
//	                      board is on screen.
//	busy_timeout(5000)    wait rather than fail if a write is already in flight.
//	                      Belt and braces next to the single-connection writer
//	                      pool: a checkpoint can still hold a lock briefly.
//	foreign_keys(ON)      OFF is SQLite's default. Without this every REFERENCES
//	                      clause in the schema is a comment.
//	synchronous(NORMAL)   fsync on checkpoint rather than on commit. Safe under
//	                      WAL against process crash; a power cut can lose the
//	                      last commits, which for a board is the right trade.
var pragmas = []string{
	"journal_mode(WAL)",
	"busy_timeout(5000)",
	"foreign_keys(ON)",
	"synchronous(NORMAL)",
}

// dsn builds the connection string for a database file. Callers pass a plain
// filesystem path; ":memory:" is rejected because the two pools would each get
// their own private empty database.
func dsn(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("database: path is empty")
	}
	if path == ":memory:" || strings.Contains(path, "mode=memory") {
		return "", fmt.Errorf(
			"database: an in-memory database cannot back two pools; use a temp file")
	}

	q := make(url.Values, len(pragmas))
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode(), nil
}
