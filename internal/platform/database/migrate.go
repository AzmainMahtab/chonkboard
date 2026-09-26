package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/pressly/goose/v3"

	"github.com/AzmainMahtab/chonkboard/migrations"
)

// gooseDialect names the dialect for goose's own bookkeeping table. Only that
// table is dialect-specific; the migration bodies in migrations/ are portable,
// so switching to PostgreSQL means changing this string and nothing in the SQL.
const gooseDialect = "sqlite3"

// migrationsDir is the root inside the embedded filesystem.
const migrationsDir = "."

// gooseLogger routes goose's output through slog so migrations at boot look like
// every other log line. goose's default logger writes to stdout and calls
// os.Exit on Fatalf, which is not acceptable inside a server process.
type gooseLogger struct{ log *slog.Logger }

func (g gooseLogger) Printf(format string, v ...any) {
	g.log.Info(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
}

func (g gooseLogger) Fatalf(format string, v ...any) {
	g.log.Error(strings.TrimRight(fmt.Sprintf(format, v...), "\n"))
}

// configureGoose points goose at the embedded migrations. It is called by every
// entry point below rather than from an init so that the logger can be injected.
func configureGoose(log *slog.Logger, dialect string) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(gooseLogger{log: log})
	if err := goose.SetDialect(dialect); err != nil {
		return fmt.Errorf("database: goose dialect %q: %w", dialect, err)
	}
	return nil
}

// ApplyMigrations runs the embedded migrations against any database/sql handle in
// any dialect goose knows.
//
// It exists so the dialect is an argument in exactly one place. The migration
// bodies are written against the DDL subset SQLite and PostgreSQL both accept, and
// the portability test in postgres_test.go proves that by applying this same set
// to a real PostgreSQL server. Everything else in this file passes gooseDialect.
func ApplyMigrations(ctx context.Context, sqlDB *sql.DB, dialect string, log *slog.Logger) error {
	if err := configureGoose(log, dialect); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, sqlDB, migrationsDir); err != nil {
		return fmt.Errorf("database: migrate up (%s): %w", dialect, err)
	}
	return nil
}

// RollBackMigrations rolls every migration back, in any dialect. Used by the
// portability test to prove each Down section is real on PostgreSQL too.
func RollBackMigrations(ctx context.Context, sqlDB *sql.DB, dialect string, log *slog.Logger) error {
	if err := configureGoose(log, dialect); err != nil {
		return err
	}
	if err := goose.DownToContext(ctx, sqlDB, migrationsDir, 0); err != nil {
		return fmt.Errorf("database: migrate reset (%s): %w", dialect, err)
	}
	return nil
}

// Migrate applies every pending migration. It runs on the writer pool because
// migrations write, and because the single connection guarantees no other
// statement interleaves with the DDL.
//
// Boot calls this and so does `chonkboard migrate up`, so there is exactly one
// migration code path and no separate goose binary in the image.
func Migrate(ctx context.Context, db *DB, log *slog.Logger) error {
	if err := configureGoose(log, gooseDialect); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db.writer.DB, migrationsDir); err != nil {
		return fmt.Errorf("database: migrate up: %w", err)
	}
	return nil
}

// MigrateDown rolls back exactly one migration.
func MigrateDown(ctx context.Context, db *DB, log *slog.Logger) error {
	if err := configureGoose(log, gooseDialect); err != nil {
		return err
	}
	if err := goose.DownContext(ctx, db.writer.DB, migrationsDir); err != nil {
		return fmt.Errorf("database: migrate down: %w", err)
	}
	return nil
}

// MigrateReset rolls every migration back, leaving an empty database.
func MigrateReset(ctx context.Context, db *DB, log *slog.Logger) error {
	if err := configureGoose(log, gooseDialect); err != nil {
		return err
	}
	if err := goose.DownToContext(ctx, db.writer.DB, migrationsDir, 0); err != nil {
		return fmt.Errorf("database: migrate reset: %w", err)
	}
	return nil
}

// MigrateStatus prints each migration and whether it has been applied.
func MigrateStatus(ctx context.Context, db *DB, log *slog.Logger) error {
	if err := configureGoose(log, gooseDialect); err != nil {
		return err
	}
	if err := goose.StatusContext(ctx, db.writer.DB, migrationsDir); err != nil {
		return fmt.Errorf("database: migrate status: %w", err)
	}
	return nil
}

// MigrateVersion reports the currently applied version.
func MigrateVersion(ctx context.Context, db *DB, log *slog.Logger) (int64, error) {
	if err := configureGoose(log, gooseDialect); err != nil {
		return 0, err
	}
	v, err := goose.GetDBVersionContext(ctx, db.writer.DB)
	if err != nil {
		return 0, fmt.Errorf("database: migrate version: %w", err)
	}
	return v, nil
}

var (
	versionPrefix = regexp.MustCompile(`^(\d+)_`)
	nameSanitiser = regexp.MustCompile(`[^a-z0-9]+`)
)

const migrationTemplate = `-- +goose Up


-- +goose Down

`

// CreateMigration writes a new empty migration into dir, numbered one past the
// highest already there.
//
// This is hand-rolled rather than goose.Create because goose writes through the
// filesystem it was given, and ours is an embed.FS that cannot be written to.
// Sequential numbering is also easier to read in review than goose's default
// timestamps, and the schema is small enough that two people numbering
// concurrently is not a real risk.
func CreateMigration(dir, name string) (string, error) {
	slug := nameSanitiser.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "_")
	slug = strings.Trim(slug, "_")
	if slug == "" {
		return "", fmt.Errorf("database: migration name is empty")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("database: read %s: %w", dir, err)
	}

	var highest int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		m := versionPrefix.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		if v, err := strconv.ParseInt(m[1], 10, 64); err == nil && v > highest {
			highest = v
		}
	}

	path := filepath.Join(dir, fmt.Sprintf("%05d_%s.sql", highest+1, slug))
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("database: %s already exists", path)
	}
	if err := os.WriteFile(path, []byte(migrationTemplate), 0o644); err != nil {
		return "", fmt.Errorf("database: write %s: %w", path, err)
	}
	return path, nil
}

// MigrationFiles lists the embedded migrations in order. Used by a test that
// asserts the embedded set matches what is on disk, so a migration added but not
// rebuilt cannot go unnoticed.
func MigrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, migrationsDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
