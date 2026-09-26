// Package database owns the connection to the one SQLite file: its pools, its
// transaction manager, its portable timestamp type, and the translation of
// driver errors into the application error model.
//
// Everything dialect-specific is confined to this package, and every file that
// carries dialect knowledge says so at the top. The SQL in migrations/ and in
// the stores is written against the subset SQLite and PostgreSQL both accept, so
// moving to PostgreSQL is a change here and nowhere else.
package database

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite" // database/sql driver, registered as "sqlite"
)

func init() {
	// sqlx ships bind types for "sqlite3" and "nrsqlite3" but not for
	// modernc's "sqlite", so BindType would report UNKNOWN and Rebind would
	// hand the query back untouched. That happens to work for SQLite, where
	// the placeholder already is '?', which is exactly why it has to be
	// registered explicitly: the day the driver becomes "pgx", Rebind must
	// start emitting $1 or every query silently breaks.
	sqlx.BindDriver(DriverName, sqlx.QUESTION)
}

// Config is the subset of application configuration this package needs.
type Config struct {
	// Path is the SQLite file. Its directory must exist.
	Path string
	// MaxReaders caps the read pool. Zero means one per CPU.
	MaxReaders int
	// ConnMaxLifetime recycles pooled connections. Zero means never.
	ConnMaxLifetime time.Duration
}

// DB holds the two pools that share one database file.
//
// SQLite permits exactly one writer at a time. A single pool with the default
// unlimited connections therefore produces SQLITE_BUSY under concurrent writes —
// a failure that never shows up at a desk and reliably shows up in use. Pinning
// the writer to one connection turns that contention into a queue inside Go,
// where it is a short wait instead of an error. Readers stay parallel because WAL
// lets them read while a write is in flight.
//
// On PostgreSQL both fields would point at one pool; nothing above this package
// would change.
type DB struct {
	writer *sqlx.DB
	reader *sqlx.DB
}

// Open connects both pools and verifies the pragmas took effect.
func Open(ctx context.Context, cfg Config) (*DB, error) {
	source, err := dsn(cfg.Path)
	if err != nil {
		return nil, err
	}

	maxReaders := cfg.MaxReaders
	if maxReaders <= 0 {
		maxReaders = runtime.NumCPU()
	}

	writer, err := sqlx.Open(DriverName, source)
	if err != nil {
		return nil, fmt.Errorf("database: open writer: %w", err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	writer.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	reader, err := sqlx.Open(DriverName, source)
	if err != nil {
		_ = writer.Close()
		return nil, fmt.Errorf("database: open reader: %w", err)
	}
	reader.SetMaxOpenConns(maxReaders)
	reader.SetMaxIdleConns(maxReaders)
	reader.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	db := &DB{writer: writer, reader: reader}

	if err := db.Ping(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	// A silently-off foreign_keys pragma makes every REFERENCES clause
	// decoration, so it is checked rather than assumed.
	if err := db.verifyPragmas(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// Writer returns the serialised write pool. Every INSERT, UPDATE and DELETE goes
// through it.
func (d *DB) Writer() *sqlx.DB { return d.writer }

// Reader returns the concurrent read pool. Use it for anything that does not
// write — but never inside a transaction, where it would be a second connection
// that cannot see the transaction's own uncommitted rows. TxManager handles that
// distinction; prefer it.
func (d *DB) Reader() *sqlx.DB { return d.reader }

// Ping checks both pools.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.writer.PingContext(ctx); err != nil {
		return fmt.Errorf("database: ping writer: %w", err)
	}
	if err := d.reader.PingContext(ctx); err != nil {
		return fmt.Errorf("database: ping reader: %w", err)
	}
	return nil
}

// Close shuts both pools down, returning the first error.
func (d *DB) Close() error {
	rerr := d.reader.Close()
	werr := d.writer.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

// verifyPragmas asserts the DSN actually applied what dsn() asked for, on both
// pools, because a pragma that failed to parse is silently ignored by SQLite.
func (d *DB) verifyPragmas(ctx context.Context) error {
	want := map[string]string{
		"journal_mode": "wal",
		"foreign_keys": "1",
		"busy_timeout": "5000",
	}
	for name, pool := range map[string]*sqlx.DB{"writer": d.writer, "reader": d.reader} {
		for pragma, expected := range want {
			var got string
			if err := pool.GetContext(ctx, &got, "PRAGMA "+pragma); err != nil {
				return fmt.Errorf("database: read PRAGMA %s on %s pool: %w", pragma, name, err)
			}
			if got != expected {
				return fmt.Errorf(
					"database: PRAGMA %s is %q on the %s pool, want %q",
					pragma, got, name, expected)
			}
		}
	}
	return nil
}
