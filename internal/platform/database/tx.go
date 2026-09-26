package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jmoiron/sqlx"
)

// Executor is what a store talks to. Both *sqlx.DB and *sqlx.Tx satisfy it, so
// a store never knows whether it is inside a transaction.
//
// Queries are written with named parameters (:uuid) rather than placeholders,
// because ':uuid' is the same text on every dialect while '?' and '$1' are not.
// sqlx rewrites them per driver, which is what makes the store SQL portable.
type Executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	GetContext(ctx context.Context, dest any, query string, args ...any) error
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
	QueryRowxContext(ctx context.Context, query string, args ...any) *sqlx.Row
	QueryxContext(ctx context.Context, query string, args ...any) (*sqlx.Rows, error)
	NamedExecContext(ctx context.Context, query string, arg any) (sql.Result, error)
	Rebind(query string) string
	DriverName() string
}

var (
	_ Executor = (*sqlx.DB)(nil)
	_ Executor = (*sqlx.Tx)(nil)
)

// txKey carries the active transaction on the context so that a service can
// compose several store calls into one atomic unit without any of them taking a
// transaction parameter.
type txKey struct{}

// TxManager hands out executors and runs transactions.
type TxManager struct {
	db *DB
}

// NewTxManager wires a manager to a database.
func NewTxManager(db *DB) *TxManager { return &TxManager{db: db} }

// Writer returns the executor a mutating statement should use: the active
// transaction if there is one, otherwise the serialised write pool.
func (m *TxManager) Writer(ctx context.Context) Executor {
	if tx, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return tx
	}
	return m.db.writer
}

// Reader returns the executor a query should use.
//
// Inside a transaction this deliberately returns the transaction, not the read
// pool. The read pool is a different connection and cannot see the
// transaction's uncommitted rows, so a store that read around its own
// transaction would see stale data — the kind of bug that only appears once two
// writes are composed.
func (m *TxManager) Reader(ctx context.Context) Executor {
	if tx, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return tx
	}
	return m.db.reader
}

// InTx runs fn inside a transaction on the write pool, committing if it returns
// nil and rolling back otherwise. The context passed to fn carries the
// transaction, so any store called with it joins the same unit of work.
//
// A nested InTx joins the transaction already in flight rather than opening a
// second one: SQLite has no nested transactions, and a second Begin on the
// one-connection writer pool would deadlock waiting for itself.
func (m *TxManager) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := ctx.Value(txKey{}).(*sqlx.Tx); ok {
		return fn(ctx)
	}

	tx, err := m.db.writer.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("database: begin: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			// Rollback after a successful Commit returns ErrTxDone; the
			// committed flag keeps that out of the logs.
			_ = tx.Rollback()
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("database: commit: %w", err)
	}
	committed = true
	return nil
}
