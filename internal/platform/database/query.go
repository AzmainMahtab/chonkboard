package database

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmoiron/sqlx"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// The three helpers below are how every store runs a query. They exist for one
// reason beyond convenience: they give the whole application a single definition
// of absence.
//
// Named parameters (:uuid) are bound and then rebound for the registered driver,
// so the SQL a store holds is dialect-neutral text. On SQLite the rebind is a
// no-op; on PostgreSQL the same call emits $1, $2.

// GetNamed runs a query expected to return at most one row.
//
// It reports found=false rather than an error when there is no row, because a
// store's job is to say whether the row is there, not to decide what that means.
// sql.ErrNoRows never escapes a store.
func GetNamed(ctx context.Context, e Executor, dest any, query string, arg any) (found bool, err error) {
	bound, args, err := sqlx.Named(query, arg)
	if err != nil {
		return false, MapError(err, "bind query")
	}
	if err := e.GetContext(ctx, dest, e.Rebind(bound), args...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, MapError(err, "query")
	}
	return true, nil
}

// SelectNamed runs a query into a slice. An empty result is an empty slice, never
// an error.
func SelectNamed(ctx context.Context, e Executor, dest any, query string, arg any) error {
	bound, args, err := sqlx.Named(query, arg)
	if err != nil {
		return MapError(err, "bind query")
	}
	return MapError(e.SelectContext(ctx, dest, e.Rebind(bound), args...), "query")
}

// ExecNamed runs a statement and reports how many rows it changed.
//
// The count is the return value rather than something the caller has to dig out
// of a sql.Result, because ignoring it is how an UPDATE that matched nothing gets
// mistaken for one that worked.
func ExecNamed(ctx context.Context, e Executor, query string, arg any) (int64, error) {
	res, err := e.NamedExecContext(ctx, query, arg)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, MapError(err, "rows affected")
	}
	return n, nil
}

// SelectIn runs a query whose WHERE clause matches a list, expanding the list
// into the right number of placeholders.
//
// A named parameter cannot hold a slice, so "WHERE uuid IN (?)" has to be
// expanded before it is rebound. This is the one place a query is written with a
// positional placeholder instead of a name.
func SelectIn(ctx context.Context, e Executor, dest any, query string, args ...any) error {
	expanded, expandedArgs, err := sqlx.In(query, args...)
	if err != nil {
		return MapError(err, "expand IN clause")
	}
	return MapError(e.SelectContext(ctx, dest, e.Rebind(expanded), expandedArgs...), "query")
}

// RequireRow turns a statement that changed nothing into a not-found error.
//
// An UPDATE or DELETE matching no rows means the row went away under the caller.
// Reporting that as success loses the write with no trace, which is why every
// mutating store method runs its row count through here.
func RequireRow(affected int64, message string) error {
	if affected == 0 {
		return apperrors.NotFound(message)
	}
	return nil
}
