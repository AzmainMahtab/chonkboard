package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/admin"
	"github.com/AzmainMahtab/chonkboard/internal/auth"
	"github.com/AzmainMahtab/chonkboard/internal/board"
	"github.com/AzmainMahtab/chonkboard/internal/card"
	"github.com/AzmainMahtab/chonkboard/internal/platform/config"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/platform/filestore"
	"github.com/AzmainMahtab/chonkboard/internal/platform/httpserver"
	"github.com/AzmainMahtab/chonkboard/internal/platform/middleware"
	"github.com/AzmainMahtab/chonkboard/internal/project"
	"github.com/AzmainMahtab/chonkboard/internal/shared/password"
	"github.com/AzmainMahtab/chonkboard/internal/shared/ratelimit"
	"github.com/AzmainMahtab/chonkboard/internal/shared/sse"
	"github.com/AzmainMahtab/chonkboard/web"
)

// Sign-in throttle, split by what is being protected.
//
// The per-account bucket is the one that bounds a guessing attack: five attempts
// refilling over a minute is invisible to somebody who mistyped and caps a single
// account at roughly 7200 guesses a day against a 12-character minimum.
//
// The per-address bucket is wider because an address is shared — a team behind one
// office NAT is many legitimate people — and its job is only to stop one host
// hammering the form.
const (
	loginBurstPerEmail = 5
	loginBurstPerIP    = 30
	loginWindow        = time.Minute
	loginBucketTTL     = 15 * time.Minute
)

// housekeepingInterval is how often idle rate-limit buckets and expired session
// rows are cleared out.
const housekeepingInterval = 10 * time.Minute

// requestEnvelopeSlack is how far a request body may exceed one file's ceiling: the
// multipart envelope, the boundary markers and the other form fields.
const requestEnvelopeSlack = 1 << 20

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	db, err := openDatabase(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("closing database", "error", err)
		}
	}()

	// Migrations run at boot, not from an entrypoint script. The schema is
	// embedded in this binary, so there is nothing to sequence and no version
	// skew between the code and the tables it expects.
	if err := database.Migrate(ctx, db, log); err != nil {
		return err
	}

	tx := database.NewTxManager(db)

	authStore := auth.NewStore(tx)
	authService, err := auth.NewService(
		authStore, tx, password.NewArgon2id(),
		auth.ServiceConfig{SessionTTL: cfg.SessionTTL}, log,
	)
	if err != nil {
		return err
	}

	if err := bootstrapSuperAdmin(ctx, authService, cfg, log); err != nil {
		return err
	}

	// The board's shape. project needs board to seed a new project's lanes, and
	// board needs project to resolve who may touch which board -- so the
	// services are wired in that order and the handlers cross-reference.
	boardService := board.NewService(board.NewStore(tx), tx, log)
	projectService := project.NewService(
		project.NewStore(tx), authService, boardService, tx, log)

	hub := sse.NewHub(log)
	projectHandler := project.NewHandler(projectService, nil, hub,
		project.HandlerConfig{AssetSuffix: web.AssetSuffix()}, log)
	boardHandler := board.NewHandler(boardService, projectHandler, log)
	// The board handler is what supplies a project's lane shape. Set after
	// construction because the two handlers genuinely need each other: one owns
	// access resolution, the other owns lanes.
	projectHandler.UseBoardShape(boardHandler)

	uploads, err := filestore.NewDisk(cfg.UploadDir)
	if err != nil {
		return err
	}
	cardService := card.NewService(
		card.NewStore(tx), boardService, uploads,
		card.ServiceConfig{MaxUploadBytes: cfg.UploadMaxBytes}, tx, log)
	cardHandler := card.NewHandler(
		cardService, projectHandler, boardService,
		authService, projectService, hub, log)
	// The assignee check needs to know who can see a board, which is the project
	// slice's answer. Set after construction: project needs the card slice for
	// nothing, but card needs project for both access and membership.
	cardService.UseMembers(projectService)

	adminHandler := admin.NewHandler(
		admin.NewService(authService, projectService, log),
		admin.HandlerConfig{AssetSuffix: web.AssetSuffix()}, log)

	loginByIP := ratelimit.New(loginBurstPerIP, loginWindow)
	loginByEmail := ratelimit.New(loginBurstPerEmail, loginWindow)

	sessionCfg := middleware.SessionConfig{
		Secure:       cfg.SessionCookieSecure,
		LoginPath:    auth.LoginPath,
		PasswordPath: auth.PasswordPath,
		LogoutPath:   auth.LogoutPath,
	}

	authHandler := auth.NewHandler(authService, loginByEmail, auth.HandlerConfig{
		CookieSecure:  cfg.SessionCookieSecure,
		SessionMaxAge: int(cfg.SessionTTL.Seconds()),
		AssetSuffix:   web.AssetSuffix(),
	}, log)

	handler := httpserver.Router(httpserver.Deps{
		Log:          log,
		Hub:          hub,
		AssetSuffix:  web.AssetSuffix(),
		Auth:         authHandler,
		Projects:     projectHandler,
		BoardMgr:     boardHandler,
		Cards:        cardHandler,
		Admin:        adminHandler,
		Session:      authService,
		SessionCfg:   sessionCfg,
		LoginByIP:    loginByIP,
		LoginByEmail: loginByEmail,
		// The slack covers the multipart envelope, the boundary markers and the
		// CSRF field that travel with an upload.
		MaxRequestBytes: cfg.UploadMaxBytes + requestEnvelopeSlack,
	})

	go housekeeping(ctx, authService, log, loginByIP, loginByEmail)

	srv := httpserver.New(httpserver.Config{
		Addr:            cfg.Addr,
		ShutdownTimeout: 15 * time.Second,
	}, log, handler)

	return srv.Run(ctx)
}

// bootstrapSuperAdmin creates the operator's account on a fresh installation.
//
// The generated password is printed to stdout rather than logged, because a log is
// a file somebody else may be able to read and this value is a credential. It is
// shown once and never recoverable — losing it means resetting it, which is the
// intended path.
func bootstrapSuperAdmin(
	ctx context.Context, svc *auth.Service, cfg config.Config, log *slog.Logger,
) error {
	result, err := svc.EnsureSuperAdmin(ctx, cfg.SuperAdminEmail, cfg.SuperAdminPassword)
	if err != nil {
		return err
	}
	if !result.Created {
		return nil
	}

	log.Info("created the first super admin", "email", result.Email)
	if result.GeneratedPassword != "" {
		fmt.Printf(`
────────────────────────────────────────────────────────────────
  Chonkboard created its first account.

    email     %s
    password  %s

  This password is shown once and is not stored anywhere in
  plaintext. You will be asked to change it when you sign in.
  If you lose it before then, delete the database and start again.
────────────────────────────────────────────────────────────────

`, result.Email, result.GeneratedPassword)
	}
	return nil
}

// housekeeping clears out what would otherwise grow without limit: rate-limit
// buckets for keys nobody is using, and session rows that can no longer
// authenticate anything.
//
// Both are unbounded in an attacker's favour without this — the bucket map grows
// once per distinct IP and address seen.
func housekeeping(
	ctx context.Context, svc *auth.Service, log *slog.Logger, buckets ...*ratelimit.Limiter,
) {
	ticker := time.NewTicker(housekeepingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, b := range buckets {
				if dropped := b.Sweep(loginBucketTTL); dropped > 0 {
					log.Debug("swept rate-limit buckets", "dropped", dropped)
				}
			}
			if reaped, err := svc.ReapExpiredSessions(ctx); err != nil {
				log.Error("cannot reap expired sessions", "error", err)
			} else if reaped > 0 {
				log.Info("reaped expired sessions", "rows", reaped)
			}
		}
	}
}
