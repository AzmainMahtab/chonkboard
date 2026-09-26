// Package migrations carries the goose migration files as an embedded
// filesystem. They live at the repository root rather than under
// internal/platform/database because go:embed cannot reach outside its own
// package directory, and a top-level migrations/ is where both goose's CLI and a
// human expect to find them.
package migrations

import "embed"

// FS holds every migration. The SQL inside is written against the subset of DDL
// that SQLite and PostgreSQL both accept — see docs/PROJECT_DATABASE.md for the
// rules that keeps portable.
//
//go:embed *.sql
var FS embed.FS
