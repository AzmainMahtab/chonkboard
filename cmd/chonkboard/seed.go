package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/config"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
	"github.com/AzmainMahtab/chonkboard/internal/shared/password"
)

// timeNow is a named indirection so this file does not import time for one call.
var timeNow = time.Now

const seedUsage = `usage: chonkboard seed [user <email> [display name]]

  (no arguments)  create the super admin if the installation has none
  user            create a member account and print its password once`

// runSeed is the `chonkboard seed` subcommand.
//
// It is the operator's command-line equivalent of what the admin console will do:
// create an account and hand over a one-time password. It exists now because there
// is no console until phase 7, and without a second account the rule that matters
// most — that a member cannot touch the board's shape — cannot be exercised at all.
func runSeed(ctx context.Context, cfg config.Config, log *slog.Logger, args []string) error {
	db, err := openDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := database.Migrate(ctx, db, log); err != nil {
		return err
	}

	tx := database.NewTxManager(db)
	store := auth.NewStore(tx)
	svc, err := auth.NewService(store, tx, password.NewArgon2id(),
		auth.ServiceConfig{SessionTTL: cfg.SessionTTL}, log)
	if err != nil {
		return err
	}

	if len(args) == 0 {
		return bootstrapSuperAdmin(ctx, svc, cfg, log)
	}
	if args[0] != "user" {
		return fmt.Errorf("unknown seed command %q\n\n%s", args[0], seedUsage)
	}
	if len(args) < 2 {
		return fmt.Errorf("seed user needs an email address\n\n%s", seedUsage)
	}

	email := authdomain.NormaliseEmail(args[1])
	displayName := email
	if len(args) > 2 {
		displayName = args[2]
	}

	existing, err := store.UserByEmail(ctx, email)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("an account already uses %s", email)
	}

	// Generated, never chosen here, and shown once. The same contract the admin
	// console will have: there is no plaintext copy anywhere, so losing it means
	// resetting it.
	plaintext, err := authdomain.GeneratePassword()
	if err != nil {
		return err
	}
	hash, err := password.NewArgon2id().Hash(plaintext)
	if err != nil {
		return err
	}

	user, err := authdomain.NewUser(
		idgenerator.NewUUIDv7(), email, displayName, hash, authdomain.RoleMember, timeNow())
	if err != nil {
		return err
	}
	// They must choose their own before they can reach a board.
	user.MustChangePassword = true

	if err := store.CreateUser(ctx, user); err != nil {
		return err
	}

	fmt.Printf(`
────────────────────────────────────────────────────────────────
  Account created.

    email     %s
    password  %s

  Shown once, stored only as an Argon2id hash. They will be asked
  to choose their own password when they first sign in.

  Grant them a project from that project's settings page.
────────────────────────────────────────────────────────────────

`, email, plaintext)
	return nil
}
