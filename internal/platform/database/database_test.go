package database_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// schemaTables is every table the migrations create. The count is asserted so
// that a table added without a matching Down section is caught.
var schemaTables = []string{
	"attachments", "card_activity", "card_labels", "cards", "comments",
	"labels", "lanes", "project_members", "projects", "sessions", "users",
}

const insertUser = `
INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
VALUES (:uuid, :email, :display_name, :password_hash, :role, :created_at, :updated_at)`

func newUser(t testing.TB, db *database.DB, uuid, email string) {
	t.Helper()
	now := database.Now()
	_, err := db.Writer().NamedExecContext(context.Background(), insertUser, map[string]any{
		"uuid": uuid, "email": email, "display_name": "Test Person",
		"password_hash": "argon2id$fake", "role": "member",
		"created_at": now, "updated_at": now,
	})
	require.NoError(t, err)
}

func tableNames(t testing.TB, db *database.DB) []string {
	t.Helper()
	var names []string
	require.NoError(t, db.Reader().SelectContext(context.Background(), &names,
		`SELECT name FROM sqlite_master
		 WHERE type = 'table' AND name NOT LIKE 'sqlite_%' AND name <> 'goose_db_version'
		 ORDER BY name`))
	return names
}

func TestOpenAppliesPragmasOnBothPools(t *testing.T) {
	// The DSN is assembled with url.Values, which percent-encodes the
	// parentheses in "journal_mode(WAL)". If the driver failed to decode them
	// the pragma would be silently ignored -- SQLite does not complain about a
	// pragma it cannot parse -- and foreign keys would quietly be off.
	db := dbtest.New(t)

	for name, pool := range map[string]interface {
		GetContext(context.Context, any, string, ...any) error
	}{
		"writer": db.Writer(),
		"reader": db.Reader(),
	} {
		for pragma, want := range map[string]string{
			"journal_mode": "wal",
			"foreign_keys": "1",
			"busy_timeout": "5000",
			"synchronous":  "1",
		} {
			var got string
			require.NoError(t, pool.GetContext(context.Background(), &got, "PRAGMA "+pragma))
			assert.Equal(t, want, got, "PRAGMA %s on the %s pool", pragma, name)
		}
	}
}

func TestOpenRefusesAnInMemoryDatabase(t *testing.T) {
	// Two pools would each get their own private empty database, so the reader
	// would never see anything the writer wrote.
	for _, path := range []string{":memory:", "file:x?mode=memory"} {
		_, err := database.Open(context.Background(), database.Config{Path: path})
		assert.ErrorContains(t, err, "two pools", "path %q", path)
	}

	_, err := database.Open(context.Background(), database.Config{Path: "  "})
	assert.ErrorContains(t, err, "path is empty")
}

func TestWriterPoolIsPinnedToOneConnection(t *testing.T) {
	db := dbtest.New(t)
	assert.Equal(t, 1, db.Writer().Stats().MaxOpenConnections,
		"SQLite allows one writer; the pool must serialise rather than fail")
	assert.Equal(t, 4, db.Reader().Stats().MaxOpenConnections,
		"readers stay concurrent under WAL")
}

func TestMigrationsRoundTrip(t *testing.T) {
	// The phase gate: up, all the way down, and up again from an empty file.
	db := dbtest.Empty(t)
	ctx := context.Background()
	log := dbtest.Discard()

	require.NoError(t, database.Migrate(ctx, db, log))
	assert.Equal(t, schemaTables, tableNames(t, db), "after the first up")

	version, err := database.MigrateVersion(ctx, db, log)
	require.NoError(t, err)
	assert.Equal(t, int64(5), version)

	require.NoError(t, database.MigrateReset(ctx, db, log), "every Down must be real")
	assert.Empty(t, tableNames(t, db), "a Down section left a table behind")

	require.NoError(t, database.Migrate(ctx, db, log))
	assert.Equal(t, schemaTables, tableNames(t, db), "after the second up")
}

func TestMigrateDownRollsBackOneStep(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	log := dbtest.Discard()

	require.NoError(t, database.MigrateDown(ctx, db, log))

	version, err := database.MigrateVersion(ctx, db, log)
	require.NoError(t, err)
	assert.Equal(t, int64(4), version)
	assert.NotContains(t, tableNames(t, db), "card_activity")
}

func TestEmbeddedMigrationsMatchTheDiskDirectory(t *testing.T) {
	// go:embed snapshots at build time. A migration added to migrations/ but
	// never rebuilt would be invisible at runtime.
	embedded, err := database.MigrationFiles()
	require.NoError(t, err)

	onDisk, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.sql"))
	require.NoError(t, err)
	for i, p := range onDisk {
		onDisk[i] = filepath.Base(p)
	}
	sort.Strings(embedded)
	sort.Strings(onDisk)

	assert.Equal(t, onDisk, embedded, "run `make build` after adding a migration")
	assert.Len(t, embedded, 5)
}

func TestForeignKeysAreEnforced(t *testing.T) {
	// This test exists only to prove the pragma is on. Without it every
	// REFERENCES clause in the schema is decoration.
	db := dbtest.New(t)
	now := database.Now()

	_, err := db.Writer().NamedExecContext(context.Background(), `
		INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
		                      created_at, last_used_at, expires_at)
		VALUES (:uuid, :user_uuid, :token_hash, :csrf_token,
		        :created_at, :last_used_at, :expires_at)`,
		map[string]any{
			"uuid": "s1", "user_uuid": "nobody-at-all", "token_hash": "h",
			"csrf_token": "c", "created_at": now, "last_used_at": now,
			"expires_at": now,
		})

	require.Error(t, err)
	assert.True(t, database.IsForeignKeyViolation(err), "got %v", err)
}

func TestOnDeleteCascadeRemovesChildren(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	newUser(t, db, "u1", "owner@example.com")

	now := database.Now()
	_, err := db.Writer().NamedExecContext(ctx, `
		INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
		                      created_at, last_used_at, expires_at)
		VALUES (:uuid, :user_uuid, :h, :c, :now, :now, :now)`,
		map[string]any{"uuid": "s1", "user_uuid": "u1", "h": "hash", "c": "csrf", "now": now})
	require.NoError(t, err)

	_, err = db.Writer().ExecContext(ctx, `DELETE FROM users WHERE uuid = ?`, "u1")
	require.NoError(t, err)

	var remaining int
	require.NoError(t, db.Reader().GetContext(ctx, &remaining,
		`SELECT count(*) FROM sessions WHERE user_uuid = ?`, "u1"))
	assert.Zero(t, remaining, "sessions should cascade with their user")
}

func TestTimestampRoundTripsThroughARealColumn(t *testing.T) {
	// modernc.org/sqlite does not understand a TIMESTAMPTZ declared type, so a
	// bare time.Time would be stored as Go's time.String() and fail to scan
	// back. database.Time is what makes the column work.
	db := dbtest.New(t)
	ctx := context.Background()

	want := time.Date(2026, 4, 5, 6, 7, 8, 910_111_000, time.UTC)
	now := database.NewTime(want)

	_, err := db.Writer().NamedExecContext(ctx, insertUser, map[string]any{
		"uuid": "u1", "email": "ts@example.com", "display_name": "T",
		"password_hash": "h", "role": "member", "created_at": now, "updated_at": now,
	})
	require.NoError(t, err)

	var got database.Time
	require.NoError(t, db.Reader().GetContext(ctx, &got,
		`SELECT created_at FROM users WHERE uuid = ?`, "u1"))
	assert.True(t, want.Truncate(time.Microsecond).Equal(got.Time()),
		"want %s got %s", want, got)

	var stored string
	require.NoError(t, db.Reader().GetContext(ctx, &stored,
		`SELECT created_at FROM users WHERE uuid = ?`, "u1"))
	assert.Equal(t, "2026-04-05T06:07:08.910111Z", stored,
		"the stored form must be portable ISO-8601, not Go's time.String()")
}

func TestNullTimestampRoundTripsThroughARealColumn(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	newUser(t, db, "u1", "n@example.com")

	now := database.Now()
	_, err := db.Writer().NamedExecContext(ctx, `
		INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
		                      created_at, last_used_at, expires_at)
		VALUES (:uuid, :user_uuid, :h, :c, :now, :now, :now)`,
		map[string]any{"uuid": "s1", "user_uuid": "u1", "h": "hash", "c": "csrf", "now": now})
	require.NoError(t, err)

	var revoked database.NullTime
	require.NoError(t, db.Reader().GetContext(ctx, &revoked,
		`SELECT revoked_at FROM sessions WHERE uuid = ?`, "s1"))
	assert.False(t, revoked.Valid, "a session starts un-revoked")
	assert.Nil(t, revoked.Ptr())

	revokedAt := time.Date(2026, 8, 9, 10, 11, 12, 131_415_000, time.UTC)
	_, err = db.Writer().NamedExecContext(ctx,
		`UPDATE sessions SET revoked_at = :at WHERE uuid = :uuid`,
		map[string]any{"at": database.NewNullTime(revokedAt), "uuid": "s1"})
	require.NoError(t, err)

	require.NoError(t, db.Reader().GetContext(ctx, &revoked,
		`SELECT revoked_at FROM sessions WHERE uuid = ?`, "s1"))
	require.True(t, revoked.Valid)
	assert.True(t, revokedAt.Equal(*revoked.Ptr()))
}

func TestOrderByTimestampIsChronological(t *testing.T) {
	// Fixed-width rendering is the whole reason this holds: SQLite compares
	// these columns as text.
	db := dbtest.New(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Deliberately inserted out of order, and with fractions that a trimming
	// format would render at different widths.
	offsets := []time.Duration{
		2 * time.Second,
		500 * time.Millisecond,
		0,
		1500 * time.Millisecond,
		123456 * time.Microsecond,
	}
	for i, off := range offsets {
		newUserAt(t, db, fmt.Sprintf("u%d", i), fmt.Sprintf("u%d@example.com", i), base.Add(off))
	}

	var got []database.Time
	require.NoError(t, db.Reader().SelectContext(ctx, &got,
		`SELECT created_at FROM users ORDER BY created_at ASC`))
	require.Len(t, got, len(offsets))

	for i := 1; i < len(got); i++ {
		assert.False(t, got[i].Time().Before(got[i-1].Time()),
			"row %d (%s) sorted before row %d (%s)", i, got[i], i-1, got[i-1])
	}
	assert.Equal(t, base, got[0].Time())
	assert.Equal(t, base.Add(2*time.Second), got[len(got)-1].Time())
}

func newUserAt(t testing.TB, db *database.DB, uuid, email string, at time.Time) {
	t.Helper()
	ts := database.NewTime(at)
	_, err := db.Writer().NamedExecContext(context.Background(), insertUser, map[string]any{
		"uuid": uuid, "email": email, "display_name": "T", "password_hash": "h",
		"role": "member", "created_at": ts, "updated_at": ts,
	})
	require.NoError(t, err)
}

func TestBooleanColumnsRoundTrip(t *testing.T) {
	// BOOLEAN with TRUE/FALSE literals is valid in both engines; SQLite stores
	// 0/1 and database/sql converts back to bool.
	db := dbtest.New(t)
	ctx := context.Background()
	newUser(t, db, "u1", "b@example.com")

	var mustChange bool
	require.NoError(t, db.Reader().GetContext(ctx, &mustChange,
		`SELECT must_change_password FROM users WHERE uuid = ?`, "u1"))
	assert.False(t, mustChange, "DEFAULT FALSE")

	_, err := db.Writer().ExecContext(ctx,
		`UPDATE users SET must_change_password = TRUE WHERE uuid = ?`, "u1")
	require.NoError(t, err)

	require.NoError(t, db.Reader().GetContext(ctx, &mustChange,
		`SELECT must_change_password FROM users WHERE uuid = ?`, "u1"))
	assert.True(t, mustChange)
}

func TestMapErrorTranslatesConstraintViolations(t *testing.T) {
	db := dbtest.New(t)
	ctx := context.Background()
	newUser(t, db, "u1", "taken@example.com")

	tests := []struct {
		name      string
		statement string
		args      map[string]any
		wantCode  apperrors.Code
		predicate func(error) bool
	}{
		{
			name:      "duplicate email is a conflict",
			statement: insertUser,
			args: map[string]any{
				"uuid": "u2", "email": "taken@example.com", "display_name": "T",
				"password_hash": "h", "role": "member",
				"created_at": database.Now(), "updated_at": database.Now(),
			},
			wantCode:  apperrors.CodeConflict,
			predicate: database.IsUniqueViolation,
		},
		{
			name:      "duplicate primary key is a conflict",
			statement: insertUser,
			args: map[string]any{
				"uuid": "u1", "email": "other@example.com", "display_name": "T",
				"password_hash": "h", "role": "member",
				"created_at": database.Now(), "updated_at": database.Now(),
			},
			wantCode:  apperrors.CodeConflict,
			predicate: database.IsUniqueViolation,
		},
		{
			name:      "unknown role fails the CHECK and is invalid",
			statement: insertUser,
			args: map[string]any{
				"uuid": "u3", "email": "role@example.com", "display_name": "T",
				"password_hash": "h", "role": "wizard",
				"created_at": database.Now(), "updated_at": database.Now(),
			},
			wantCode:  apperrors.CodeInvalid,
			predicate: database.IsCheckViolation,
		},
		{
			name:      "an email that is not lower-cased fails the CHECK",
			statement: insertUser,
			args: map[string]any{
				"uuid": "u4", "email": "Mixed@Example.com", "display_name": "T",
				"password_hash": "h", "role": "member",
				"created_at": database.Now(), "updated_at": database.Now(),
			},
			wantCode:  apperrors.CodeInvalid,
			predicate: database.IsCheckViolation,
		},
		{
			name: "a session for nobody fails the foreign key and is invalid",
			statement: `INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
			            created_at, last_used_at, expires_at)
			            VALUES (:uuid, :user_uuid, :h, :c, :now, :now, :now)`,
			args: map[string]any{
				"uuid": "s1", "user_uuid": "ghost", "h": "hash", "c": "csrf",
				"now": database.Now(),
			},
			wantCode:  apperrors.CodeInvalid,
			predicate: database.IsForeignKeyViolation,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Writer().NamedExecContext(ctx, tc.statement, tc.args)
			require.Error(t, err)
			assert.True(t, tc.predicate(err), "predicate did not match: %v", err)

			mapped := database.MapError(err, "insert")
			var appErr *apperrors.AppError
			require.True(t, errors.As(mapped, &appErr))
			assert.Equal(t, tc.wantCode, appErr.Code)
			assert.ErrorIs(t, mapped, apperrors.New(tc.wantCode, ""))
		})
	}
}

func TestMapErrorTreatsNoRowsAsNotFound(t *testing.T) {
	// An UPDATE ... RETURNING that matched nothing surfaces as ErrNoRows, and
	// that is a 404, never a 500.
	db := dbtest.New(t)

	var uuid string
	err := db.Reader().QueryRowxContext(context.Background(),
		`SELECT uuid FROM users WHERE uuid = ?`, "absent").Scan(&uuid)
	require.Error(t, err)

	mapped := database.MapError(err, "lookup")
	assert.ErrorIs(t, mapped, apperrors.New(apperrors.CodeNotFound, ""))
}

func TestMapErrorPassesAppErrorsThrough(t *testing.T) {
	original := apperrors.Conflict("that email is already in use")
	assert.Same(t, original, database.MapError(original, "insert"))
}

func TestMapErrorIsNilForNil(t *testing.T) {
	assert.NoError(t, database.MapError(nil, "insert"))
}

func TestConstraintNamesTheFailure(t *testing.T) {
	// Every CHECK in migrations/ is written with an explicit CONSTRAINT clause
	// so the driver reports a name here instead of the whole expression.
	db := dbtest.New(t)
	ctx := context.Background()
	newUser(t, db, "u1", "taken@example.com")

	_, err := db.Writer().NamedExecContext(ctx, insertUser, map[string]any{
		"uuid": "u2", "email": "taken@example.com", "display_name": "T",
		"password_hash": "h", "role": "member",
		"created_at": database.Now(), "updated_at": database.Now(),
	})
	require.Error(t, err)
	assert.Equal(t, "users.email", database.Constraint(err))

	_, err = db.Writer().NamedExecContext(ctx, insertUser, map[string]any{
		"uuid": "u3", "email": "role@example.com", "display_name": "T",
		"password_hash": "h", "role": "wizard",
		"created_at": database.Now(), "updated_at": database.Now(),
	})
	require.Error(t, err)
	assert.Equal(t, "users_role_check", database.Constraint(err))

	assert.Empty(t, database.Constraint(errors.New("not a driver error")))
}

func TestRebindTargetsTheRegisteredDialect(t *testing.T) {
	// sqlx does not know modernc's driver name out of the box, so the package
	// registers it. The assertion that matters is that Rebind is wired at all:
	// on PostgreSQL the same call has to start emitting $1.
	db := dbtest.New(t)
	assert.Equal(t, "sqlite", db.Writer().DriverName())
	assert.Equal(t,
		`SELECT * FROM users WHERE uuid = ? AND email = ?`,
		db.Writer().Rebind(`SELECT * FROM users WHERE uuid = ? AND email = ?`))
}
