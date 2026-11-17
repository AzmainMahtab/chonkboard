package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/AzmainMahtab/chonkboard/internal/platform/config"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
)

// migrateUsage is printed for an unknown subcommand.
const migrateUsage = `usage: chonkboard migrate <up|down|reset|status|version|create NAME>

  up       apply every pending migration
  down     roll back one migration
  reset    roll back every migration, leaving an empty database
  status   show which migrations have run
  version  print the applied version
  create   write a new empty migration into migrations/`

// runMigrate is the `chonkboard migrate` subcommand.
//
// It shares Migrate with boot, so there is one migration code path and no goose
// binary in the image. The migrations themselves are embedded in this executable,
// which is why a deploy cannot apply a version of the schema the code was not
// built against.
func runMigrate(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", migrateUsage)
	}

	// `create` writes a file and never touches the database, so it runs before
	// anything is opened -- useful on a machine with no data directory yet.
	if args[0] == "create" {
		if len(args) < 2 {
			return fmt.Errorf("migrate create needs a name, e.g. migrate create add_card_colours")
		}
		path, err := database.CreateMigration(migrationsSourceDir, args[1])
		if err != nil {
			return err
		}
		log.Info("migration created", "path", path)
		return nil
	}

	db, err := openDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	switch args[0] {
	case "up":
		if err := database.Migrate(ctx, db, log); err != nil {
			return err
		}
		version, err := database.MigrateVersion(ctx, db, log)
		if err != nil {
			return err
		}
		log.Info("migrations applied", "version", version)
		return nil

	case "down":
		if err := database.MigrateDown(ctx, db, log); err != nil {
			return err
		}
		version, err := database.MigrateVersion(ctx, db, log)
		if err != nil {
			return err
		}
		log.Info("migration rolled back", "version", version)
		return nil

	case "reset":
		if err := database.MigrateReset(ctx, db, log); err != nil {
			return err
		}
		log.Warn("every migration rolled back; the database is empty")
		return nil

	case "status":
		return database.MigrateStatus(ctx, db, log)

	case "version":
		version, err := database.MigrateVersion(ctx, db, log)
		if err != nil {
			return err
		}
		fmt.Println(version)
		return nil

	default:
		return fmt.Errorf("unknown migrate command %q\n\n%s", args[0], migrateUsage)
	}
}

// migrationsSourceDir is where `migrate create` writes. It is a source path
// rather than an embedded one because embed.FS is read-only; the command is a
// development tool and is expected to run from the repository root.
const migrationsSourceDir = "migrations"

// openDatabase opens the pools, creating the data directory if it is missing.
//
// Creating it here rather than requiring a pre-made volume means `docker compose
// up` on a fresh volume works, and so does a first `make run` on a clean clone.
func openDatabase(ctx context.Context, cfg config.Config, log *slog.Logger) (*database.DB, error) {
	dir := filepath.Dir(cfg.DBPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create data directory %s: %w", dir, err)
	}

	db, err := database.Open(ctx, database.Config{
		Path:            cfg.DBPath,
		MaxReaders:      cfg.DBMaxReaders,
		ConnMaxLifetime: cfg.DBConnMaxLife,
	})
	if err != nil {
		return nil, err
	}
	log.Debug("database open", "path", cfg.DBPath)
	return db, nil
}
