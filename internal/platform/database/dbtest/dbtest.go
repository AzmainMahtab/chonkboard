// Package dbtest opens migrated throwaway databases for tests.
//
// It is a separate package so that importing "testing" does not pull test flags
// into the server binary.
//
// The database is a real file in t.TempDir(), not an in-memory one: SQLite gives
// each connection its own private in-memory database unless shared-cache mode is
// used, which would defeat the two-pool setup and test something other than what
// ships. A file in a temp directory costs a millisecond.
package dbtest

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
)

// Discard is a logger that swallows goose's migration chatter.
func Discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// New opens a migrated database in a temporary directory and closes it when the
// test finishes.
func New(t testing.TB) *database.DB {
	t.Helper()

	db, err := database.Open(context.Background(), database.Config{
		Path:       filepath.Join(t.TempDir(), "test.db"),
		MaxReaders: 4,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})

	if err := database.Migrate(context.Background(), db, Discard()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// NewWithTx opens a migrated database alongside its transaction manager, which is
// what a store test needs.
func NewWithTx(t testing.TB) (*database.DB, *database.TxManager) {
	t.Helper()
	db := New(t)
	return db, database.NewTxManager(db)
}

// Empty opens an unmigrated database, for tests about migration itself.
func Empty(t testing.TB) *database.DB {
	t.Helper()

	db, err := database.Open(context.Background(), database.Config{
		Path:       filepath.Join(t.TempDir(), "empty.db"),
		MaxReaders: 2,
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
