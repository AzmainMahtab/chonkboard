package database

// This file is dialect-specific. It translates driver errors into the one
// application error model. The PostgreSQL version of it reads the same codes off
// *pgconn.PgError ("23505" for unique, "23503" for foreign key, "23514" for
// check, "23502" for not-null) and exposes the same four predicates, so nothing
// above this package changes.

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	sqlite3 "modernc.org/sqlite"
)

// SQLite extended result codes. The primary code for all of these is
// SQLITE_CONSTRAINT (19); the extended code is what says which constraint, and
// that distinction is the difference between a 409 and a 400.
const (
	codeConstraintCheck      = 275  // 19 | (1 << 8)
	codeConstraintForeignKey = 787  // 19 | (3 << 8)
	codeConstraintNotNull    = 1299 // 19 | (5 << 8)
	codeConstraintPrimaryKey = 1555 // 19 | (6 << 8)
	codeConstraintUnique     = 2067 // 19 | (8 << 8)
	codeConstraintRowID      = 2579 // 19 | (10 << 8)

	codeBusy   = 5
	codeLocked = 6
)

// resultCode extracts the driver's extended result code, or 0 if err did not
// come from the driver.
func resultCode(err error) int {
	var serr *sqlite3.Error
	if errors.As(err, &serr) {
		return serr.Code()
	}
	return 0
}

// IsUniqueViolation reports whether err is a uniqueness conflict — a duplicate
// email, a duplicate project slug, a label name already used in the project.
func IsUniqueViolation(err error) bool {
	switch resultCode(err) {
	case codeConstraintUnique, codeConstraintPrimaryKey, codeConstraintRowID:
		return true
	}
	return false
}

// IsForeignKeyViolation reports whether err is a reference to a row that does
// not exist, or a delete blocked by a child row.
func IsForeignKeyViolation(err error) bool {
	return resultCode(err) == codeConstraintForeignKey
}

// IsCheckViolation reports whether err broke a CHECK constraint — an unknown
// role, a negative position, a colour outside the palette.
func IsCheckViolation(err error) bool { return resultCode(err) == codeConstraintCheck }

// IsNotNullViolation reports whether err wrote NULL into a NOT NULL column.
func IsNotNullViolation(err error) bool { return resultCode(err) == codeConstraintNotNull }

// IsBusy reports whether the database was locked for longer than busy_timeout.
// Seeing this means the writer pool is not the only writer — a second process,
// or a pool opened without going through Open.
func IsBusy(err error) bool {
	switch resultCode(err) {
	case codeBusy, codeLocked:
		return true
	}
	return false
}

// Constraint returns the name of the constraint that failed, as the driver
// reported it: "users.email" for a unique or not-null violation, and the
// constraint name such as "users_role_check" for a CHECK — which is why every
// CHECK in migrations/ is written with an explicit CONSTRAINT clause rather than
// left anonymous. It returns "" when the driver gave no detail, as SQLite does
// for foreign keys.
func Constraint(err error) string {
	var serr *sqlite3.Error
	if !errors.As(err, &serr) {
		return ""
	}
	msg := serr.Error()
	const marker = "constraint failed: "
	// The message repeats the phrase ("constraint failed: UNIQUE constraint
	// failed: users.email"), so take the text after the last occurrence.
	idx := strings.LastIndex(msg, marker)
	if idx < 0 {
		return ""
	}
	name := msg[idx+len(marker):]
	// Trim the trailing " (2067)" the driver appends.
	if paren := strings.LastIndex(name, " ("); paren > 0 {
		name = name[:paren]
	}
	return strings.TrimSpace(name)
}

// MapError converts a driver error into an *apperrors.AppError. A store wraps
// every driver call in it so that no layer above has to know what a result code
// is.
//
// An *AppError passes through untouched, so a store may produce a better message
// of its own first — checking IsUniqueViolation and returning
// apperrors.Conflict("that email is already in use") — and still funnel
// everything through one call.
func MapError(err error, context string) error {
	if err == nil {
		return nil
	}

	var already *apperrors.AppError
	if errors.As(err, &already) {
		return err
	}

	// A query that returns no row is never an internal fault: for a lookup it
	// means absent, and for an UPDATE ... RETURNING it means the row was not
	// there to update. Stores that treat absence as a normal outcome check for
	// this before calling MapError and return (nil, nil) instead.
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.NotFound("that record no longer exists").Wrap(err)
	}

	switch {
	case IsUniqueViolation(err):
		return apperrors.Conflict("that value is already taken").Wrap(err)
	case IsForeignKeyViolation(err):
		return apperrors.Invalid("that refers to something that does not exist").Wrap(err)
	case IsCheckViolation(err):
		return apperrors.Invalid("that value is not allowed").Wrap(err)
	case IsNotNullViolation(err):
		return apperrors.Invalid("a required value was missing").Wrap(err)
	case IsBusy(err):
		return apperrors.Internal(err, context+": database is locked")
	}

	return apperrors.Internal(err, context)
}
