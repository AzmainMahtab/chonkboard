package database_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
)

func countUsers(t testing.TB, db *database.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.Reader().GetContext(context.Background(), &n,
		`SELECT count(*) FROM users`))
	return n
}

func insertUserWith(ctx context.Context, e database.Executor, uuid, email string) error {
	now := database.Now()
	_, err := e.NamedExecContext(ctx, insertUser, map[string]any{
		"uuid": uuid, "email": email, "display_name": "T", "password_hash": "h",
		"role": "member", "created_at": now, "updated_at": now,
	})
	return err
}

func TestInTxCommitsOnSuccess(t *testing.T) {
	db, tm := dbtest.NewWithTx(t)

	require.NoError(t, tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}
		return insertUserWith(ctx, tm.Writer(ctx), "u2", "b@example.com")
	}))

	assert.Equal(t, 2, countUsers(t, db))
}

func TestInTxRollsBackOnError(t *testing.T) {
	db, tm := dbtest.NewWithTx(t)
	sentinel := errors.New("deliberate")

	err := tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}
		return sentinel
	})

	assert.ErrorIs(t, err, sentinel)
	assert.Zero(t, countUsers(t, db), "the first insert must not survive")
}

func TestInTxRollsBackWhenAStatementFails(t *testing.T) {
	// The realistic case: two writes, the second breaks a constraint. Neither
	// may land. This is what a card move depends on -- renumbering two lanes is
	// all or nothing.
	db, tm := dbtest.NewWithTx(t)

	err := tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "dup@example.com"); err != nil {
			return err
		}
		return insertUserWith(ctx, tm.Writer(ctx), "u2", "dup@example.com")
	})

	require.Error(t, err)
	assert.True(t, database.IsUniqueViolation(err))
	assert.Zero(t, countUsers(t, db))
}

func TestReaderInsideTxSeesTheTransactionsOwnWrites(t *testing.T) {
	// The subtle one. The read pool is a different connection and cannot see
	// uncommitted rows, so TxManager.Reader must hand back the transaction while
	// one is active. A store that read around its own transaction would see
	// stale data -- a bug that only appears once two writes are composed.
	db, tm := dbtest.NewWithTx(t)

	require.NoError(t, tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}

		var inside int
		if err := tm.Reader(ctx).GetContext(ctx, &inside, `SELECT count(*) FROM users`); err != nil {
			return err
		}
		assert.Equal(t, 1, inside, "the transaction must see its own insert")

		var outside int
		require.NoError(t, db.Reader().GetContext(ctx, &outside, `SELECT count(*) FROM users`))
		assert.Zero(t, outside, "the read pool must not see it yet")
		return nil
	}))

	assert.Equal(t, 1, countUsers(t, db))
}

func TestWriterAndReaderOutsideTxUseThePools(t *testing.T) {
	db, tm := dbtest.NewWithTx(t)
	ctx := context.Background()

	assert.Same(t, db.Writer(), tm.Writer(ctx))
	assert.Same(t, db.Reader(), tm.Reader(ctx))
}

func TestNestedInTxJoinsTheOuterTransaction(t *testing.T) {
	// SQLite has no nested transactions, and a second Begin on the
	// one-connection writer pool would deadlock waiting for itself. The inner
	// call must join, and a failure inside it must still roll the outer back.
	db, tm := dbtest.NewWithTx(t)
	sentinel := errors.New("inner failed")

	inner := false
	err := tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}
		return tm.InTx(ctx, func(ctx context.Context) error {
			inner = true
			if err := insertUserWith(ctx, tm.Writer(ctx), "u2", "b@example.com"); err != nil {
				return err
			}
			return sentinel
		})
	})

	assert.True(t, inner, "the inner function must actually run")
	assert.ErrorIs(t, err, sentinel)
	assert.Zero(t, countUsers(t, db), "the outer transaction must roll back too")
}

func TestNestedInTxCommitsOnceThroughTheOuter(t *testing.T) {
	db, tm := dbtest.NewWithTx(t)

	require.NoError(t, tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}
		return tm.InTx(ctx, func(ctx context.Context) error {
			return insertUserWith(ctx, tm.Writer(ctx), "u2", "b@example.com")
		})
	}))

	assert.Equal(t, 2, countUsers(t, db))
}

func TestConcurrentWritesQueueRatherThanFail(t *testing.T) {
	// The reason the writer pool is pinned to one connection. With an unbounded
	// pool this is where SQLITE_BUSY appears -- never at a desk, reliably in
	// use.
	db, tm := dbtest.NewWithTx(t)

	const writers = 24
	var wg sync.WaitGroup
	errs := make([]error, writers)

	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = tm.InTx(context.Background(), func(ctx context.Context) error {
				return insertUserWith(ctx, tm.Writer(ctx),
					"u"+string(rune('a'+i)), "u"+string(rune('a'+i))+"@example.com")
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "writer %d", i)
	}
	assert.Equal(t, writers, countUsers(t, db))
}

func TestReadsStayConcurrentWithAWriteInFlight(t *testing.T) {
	// WAL's payoff: a reader is not blocked by an open write transaction. This
	// matters because an open SSE stream holds a read connection for as long as
	// a board is on screen.
	db, tm := dbtest.NewWithTx(t)

	require.NoError(t, tm.InTx(context.Background(), func(ctx context.Context) error {
		if err := insertUserWith(ctx, tm.Writer(ctx), "u1", "a@example.com"); err != nil {
			return err
		}

		done := make(chan error, 1)
		go func() {
			var n int
			done <- db.Reader().GetContext(context.Background(), &n, `SELECT count(*) FROM users`)
		}()

		select {
		case err := <-done:
			assert.NoError(t, err, "a read must not block on an open write")
		case <-ctx.Done():
			t.Fatal("read did not complete")
		}
		return nil
	}))
}
