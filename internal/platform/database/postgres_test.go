//go:build postgres

// This file proves the portability claim rather than asserting it: it applies the
// real embedded migrations to a real PostgreSQL server and round-trips the types
// the schema depends on.
//
// It is behind a build tag because it needs a server, so `make check` does not
// run it. Run it deliberately:
//
//	make check-postgres
//
// or by hand:
//
//	docker run -d --rm --name chonk-pg -e POSTGRES_PASSWORD=probe \
//	  -e POSTGRES_DB=chonkboard -p 55432:5432 postgres:17-alpine
//	CHONKBOARD_TEST_POSTGRES='postgres://postgres:probe@127.0.0.1:55432/chonkboard?sslmode=disable' \
//	  go test -tags postgres ./internal/platform/database/ -run Postgres -v
//
// If this test starts failing, a migration has drifted into dialect-specific DDL
// and the move to PostgreSQL is no longer a configuration change.
package database_test

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	"github.com/jmoiron/sqlx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
)

const postgresDialect = "postgres"

// openPostgres connects to the server named by CHONKBOARD_TEST_POSTGRES and hands
// back a clean, empty schema.
func openPostgres(t *testing.T) *sqlx.DB {
	t.Helper()

	dsn := os.Getenv("CHONKBOARD_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set CHONKBOARD_TEST_POSTGRES to run the portability test")
	}

	db, err := sqlx.Open("pgx", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	require.NoError(t, db.PingContext(ctx), "is PostgreSQL running?")

	// Each test starts from nothing, so ordering between tests cannot matter.
	_, err = db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`)
	require.NoError(t, err)

	return db
}

func postgresTables(t *testing.T, db *sqlx.DB) []string {
	t.Helper()
	var names []string
	require.NoError(t, db.Select(&names, `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		ORDER BY table_name`))
	return names
}

func TestPostgresAcceptsEveryMigration(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	log := dbtest.Discard()

	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, log),
		"the migrations must apply unchanged to PostgreSQL")

	want := []string{
		"attachments", "card_activity", "card_labels", "cards", "comments",
		"labels", "lanes", "project_members", "projects", "sessions", "users",
	}
	assert.Equal(t, want, postgresTables(t, db))
}

func TestPostgresAcceptsEveryRollback(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	log := dbtest.Discard()

	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, log))
	require.NoError(t, database.RollBackMigrations(ctx, db.DB, postgresDialect, log))

	assert.Empty(t, postgresTables(t, db), "a Down section left a table behind")
}

func TestPostgresGivesTheColumnsTheirNativeTypes(t *testing.T) {
	// The point of writing TIMESTAMPTZ and BOOLEAN rather than TEXT and INTEGER:
	// SQLite treats them as affinity hints, PostgreSQL gives them real types.
	db := openPostgres(t)
	require.NoError(t, database.ApplyMigrations(context.Background(), db.DB, postgresDialect, dbtest.Discard()))

	type column struct {
		Name string `db:"column_name"`
		Type string `db:"data_type"`
	}
	var columns []column
	require.NoError(t, db.Select(&columns, `
		SELECT column_name, data_type FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'users'`))

	byName := make(map[string]string, len(columns))
	for _, c := range columns {
		byName[c.Name] = c.Type
	}

	assert.Equal(t, "timestamp with time zone", byName["created_at"])
	assert.Equal(t, "timestamp with time zone", byName["updated_at"])
	assert.Equal(t, "boolean", byName["must_change_password"])
	assert.Equal(t, "text", byName["uuid"])
	assert.Equal(t, "text", byName["email"])
}

func TestPostgresRoundTripsTheTimestampWireFormat(t *testing.T) {
	// The one that actually matters. database.Time writes a fixed-width ISO-8601
	// string because SQLite compares these columns as text. This proves the same
	// string is accepted by a real timestamptz column and reads back equal.
	db := openPostgres(t)
	ctx := context.Background()
	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, dbtest.Discard()))

	want := time.Date(2026, 4, 5, 6, 7, 8, 910_111_000, time.UTC)

	_, err := db.NamedExecContext(ctx, `
		INSERT INTO users (uuid, email, display_name, password_hash, role,
		                   created_at, updated_at)
		VALUES (:uuid, :email, 'Test Person', 'hash', 'member', :at, :at)`,
		map[string]any{
			"uuid":  "11111111-1111-7111-8111-111111111111",
			"email": "ts@example.com", "at": database.NewTime(want),
		})
	require.NoError(t, err, "the wire format must be valid timestamptz input")

	var got database.Time
	require.NoError(t, db.Get(&got,
		`SELECT created_at FROM users WHERE email = $1`, "ts@example.com"))
	assert.True(t, want.Equal(got.Time()), "want %s got %s", want, got)
	assert.Equal(t, time.UTC, got.Time().Location())
}

func TestPostgresRoundTripsNullableTimestamps(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, dbtest.Discard()))
	seedPostgresUser(t, db, "11111111-1111-7111-8111-111111111111", "person@example.com")

	_, err := db.NamedExecContext(ctx, `
		INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
		                      created_at, last_used_at, expires_at, revoked_at)
		VALUES (:uuid, :user, 'hash', 'csrf', :at, :at, :at, :revoked)`,
		map[string]any{
			"uuid": "22222222-2222-7222-8222-222222222222",
			"user": "11111111-1111-7111-8111-111111111111",
			"at":   database.Now(), "revoked": database.NullTime{},
		})
	require.NoError(t, err)

	var revoked database.NullTime
	require.NoError(t, db.Get(&revoked, `SELECT revoked_at FROM sessions LIMIT 1`))
	assert.False(t, revoked.Valid)

	at := time.Date(2026, 8, 9, 10, 11, 12, 131_415_000, time.UTC)
	_, err = db.NamedExecContext(ctx,
		`UPDATE sessions SET revoked_at = :at`,
		map[string]any{"at": database.NewNullTime(at)})
	require.NoError(t, err)

	require.NoError(t, db.Get(&revoked, `SELECT revoked_at FROM sessions LIMIT 1`))
	require.True(t, revoked.Valid)
	assert.True(t, at.Equal(*revoked.Ptr()))
}

func TestPostgresOrdersTimestampsChronologically(t *testing.T) {
	db := openPostgres(t)
	ctx := context.Background()
	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, dbtest.Discard()))

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	offsets := []time.Duration{
		2 * time.Second, 500 * time.Millisecond, 0,
		1500 * time.Millisecond, 123456 * time.Microsecond,
	}
	for i, off := range offsets {
		_, err := db.NamedExecContext(ctx, `
			INSERT INTO users (uuid, email, display_name, password_hash, role,
			                   created_at, updated_at)
			VALUES (:uuid, :email, 'T', 'hash', 'member', :at, :at)`,
			map[string]any{
				"uuid":  "3333333" + string(rune('a'+i)) + "-3333-7333-8333-333333333333",
				"email": "u" + string(rune('a'+i)) + "@example.com",
				"at":    database.NewTime(base.Add(off)),
			})
		require.NoError(t, err)
	}

	var got []database.Time
	require.NoError(t, db.Select(&got, `SELECT created_at FROM users ORDER BY created_at ASC`))
	require.Len(t, got, len(offsets))
	for i := 1; i < len(got); i++ {
		assert.False(t, got[i].Time().Before(got[i-1].Time()))
	}
	assert.Equal(t, base, got[0].Time())
}

func TestPostgresEnforcesTheSameConstraints(t *testing.T) {
	// Foreign keys, CHECKs and UNIQUE indexes must all mean the same thing on
	// both engines. Only the error *codes* differ, which is why errors.go is the
	// one file that has to be rewritten for PostgreSQL.
	db := openPostgres(t)
	ctx := context.Background()
	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, dbtest.Discard()))

	const owner = "11111111-1111-7111-8111-111111111111"
	const project = "aaaaaaaa-aaaa-7aaa-8aaa-aaaaaaaaaaaa"
	seedPostgresUser(t, db, owner, "taken@example.com")
	_, err := db.NamedExecContext(ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'chonk', 'Chonkboard', :owner, :at, :at)`,
		map[string]any{"uuid": project, "owner": owner, "at": database.Now()})
	require.NoError(t, err)

	tests := []struct {
		name      string
		statement string
		args      map[string]any
		wantCode  string
	}{
		{
			name: "duplicate email",
			statement: `INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
			            VALUES (:uuid, 'taken@example.com', 'T', 'h', 'member', :at, :at)`,
			args:     map[string]any{"uuid": "44444444-4444-7444-8444-444444444444", "at": database.Now()},
			wantCode: "23505", // unique_violation
		},
		{
			name: "unknown role",
			statement: `INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
			            VALUES (:uuid, 'role@example.com', 'T', 'h', 'wizard', :at, :at)`,
			args:     map[string]any{"uuid": "55555555-5555-7555-8555-555555555555", "at": database.Now()},
			wantCode: "23514", // check_violation
		},
		{
			name: "email that is not lower-cased",
			statement: `INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
			            VALUES (:uuid, 'Mixed@Example.com', 'T', 'h', 'member', :at, :at)`,
			args:     map[string]any{"uuid": "66666666-6666-7666-8666-666666666666", "at": database.Now()},
			wantCode: "23514",
		},
		{
			name: "session for nobody",
			statement: `INSERT INTO sessions (uuid, user_uuid, token_hash, csrf_token,
			                                  created_at, last_used_at, expires_at)
			            VALUES (:uuid, :ghost, 'h', 'c', :at, :at, :at)`,
			args: map[string]any{
				"uuid":  "77777777-7777-7777-8777-777777777777",
				"ghost": "88888888-8888-7888-8888-888888888888", "at": database.Now(),
			},
			wantCode: "23503", // foreign_key_violation
		},
		{
			// The project is seeded, so only the CHECK is in play. Naming a
			// missing project here would report a different code on each
			// engine -- PostgreSQL evaluates CHECKs before foreign keys and
			// SQLite the reverse -- so a case that breaks two constraints at
			// once tests evaluation order rather than the schema.
			name: "lane with a zero WIP limit",
			statement: `INSERT INTO lanes (uuid, project_uuid, name, position, wip_limit, created_at, updated_at)
			            VALUES (:uuid, :project, 'broken', 0, 0, :at, :at)`,
			args: map[string]any{
				"uuid":    "99999999-9999-7999-8999-999999999999",
				"project": project, "at": database.Now(),
			},
			wantCode: "23514", // check_violation
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.NamedExecContext(ctx, tc.statement, tc.args)
			require.Error(t, err)
			assert.Equal(t, tc.wantCode, pgCode(err), "unexpected error: %v", err)
		})
	}
}

func TestPostgresAcceptsThePortableUpsert(t *testing.T) {
	// ON CONFLICT ... DO UPDATE with the excluded pseudo-table is the one upsert
	// syntax both engines share. project_members depends on it.
	db := openPostgres(t)
	ctx := context.Background()
	require.NoError(t, database.ApplyMigrations(ctx, db.DB, postgresDialect, dbtest.Discard()))

	const owner = "11111111-1111-7111-8111-111111111111"
	const person = "22222222-2222-7222-8222-222222222222"
	const project = "33333333-3333-7333-8333-333333333333"
	seedPostgresUser(t, db, owner, "owner@example.com")
	seedPostgresUser(t, db, person, "person@example.com")

	_, err := db.NamedExecContext(ctx, `
		INSERT INTO projects (uuid, slug, name, created_by, created_at, updated_at)
		VALUES (:uuid, 'chonk', 'Chonkboard', :owner, :at, :at)`,
		map[string]any{"uuid": project, "owner": owner, "at": database.Now()})
	require.NoError(t, err)

	upsert := `
		INSERT INTO project_members (project_uuid, user_uuid, role, granted_by, granted_at)
		VALUES (:project, :person, :role, :owner, :at)
		ON CONFLICT (project_uuid, user_uuid) DO UPDATE SET
			role       = excluded.role,
			granted_by = excluded.granted_by,
			granted_at = excluded.granted_at`

	for _, role := range []string{"member", "manager"} {
		_, err := db.NamedExecContext(ctx, upsert, map[string]any{
			"project": project, "person": person, "role": role,
			"owner": owner, "at": database.Now(),
		})
		require.NoError(t, err, "role %s", role)
	}

	var role string
	require.NoError(t, db.Get(&role, `SELECT role FROM project_members`))
	assert.Equal(t, "manager", role)

	var rows int
	require.NoError(t, db.Get(&rows, `SELECT count(*) FROM project_members`))
	assert.Equal(t, 1, rows, "the upsert must not have inserted a second row")
}

func TestPostgresHasThePartialIndex(t *testing.T) {
	db := openPostgres(t)
	require.NoError(t, database.ApplyMigrations(context.Background(), db.DB, postgresDialect, dbtest.Discard()))

	var indexes []string
	require.NoError(t, db.Select(&indexes,
		`SELECT indexname FROM pg_indexes WHERE schemaname = 'public' ORDER BY indexname`))
	sort.Strings(indexes)

	assert.Contains(t, indexes, "cards_project_live_idx",
		"the partial index on live cards must exist on PostgreSQL too")

	var definition string
	require.NoError(t, db.Get(&definition,
		`SELECT indexdef FROM pg_indexes WHERE indexname = 'cards_project_live_idx'`))
	assert.Contains(t, definition, "WHERE (archived_at IS NULL)")
}

func seedPostgresUser(t *testing.T, db *sqlx.DB, uuid, email string) {
	t.Helper()
	_, err := db.NamedExecContext(context.Background(), `
		INSERT INTO users (uuid, email, display_name, password_hash, role, created_at, updated_at)
		VALUES (:uuid, :email, 'Test Person', 'hash', 'member', :at, :at)`,
		map[string]any{"uuid": uuid, "email": email, "at": database.Now()})
	require.NoError(t, err)
}

// pgCode extracts the SQLSTATE PostgreSQL reported.
//
// The codes are the whole reason errors.go is the one file that must be rewritten
// for PostgreSQL: the constraints mean the same thing, but 23505 is what
// PostgreSQL calls what SQLite calls 2067.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
