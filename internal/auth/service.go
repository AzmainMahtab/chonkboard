package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
	"github.com/AzmainMahtab/chonkboard/internal/shared/password"
)

// The failures this service reports. Compare with errors.Is; the messages are
// what a user sees, so they say nothing a stranger could learn from.
var (
	// ErrInvalidCredentials is returned for a wrong password *and* for an
	// unknown address, with the same message and the same cost either way. Two
	// distinguishable outcomes would let anyone test whether an address has an
	// account here.
	ErrInvalidCredentials = apperrors.Unauthorized("That email or password is wrong.")

	// ErrAccountSuspended is deliberately distinct: it is only ever returned
	// after the password already verified, so it reveals nothing to someone who
	// does not already hold the credential.
	ErrAccountSuspended = apperrors.Forbidden("That account has been suspended.")

	// ErrNoSession means the cookie matched nothing.
	ErrNoSession = apperrors.Unauthorized("Please sign in.")

	// ErrSessionEnded means the session was revoked or has expired.
	ErrSessionEnded = apperrors.Unauthorized("Your session has ended. Please sign in again.")

	// ErrSamePassword refuses a "change" that changes nothing.
	ErrSamePassword = apperrors.Validation("That is already your password.").
			WithField("password", "must be different from the current one")
)

// ServiceConfig is what the service needs from application configuration.
type ServiceConfig struct {
	SessionTTL time.Duration
}

// Service owns sessions and passwords.
type Service struct {
	store  *Store
	tx     *database.TxManager
	hasher password.Hasher
	cfg    ServiceConfig
	log    *slog.Logger

	// now is injectable so session expiry can be tested without sleeping.
	now func() time.Time

	// decoyHash is verified against when no account matches, so a login attempt
	// for an unknown address costs the same as one for a known address with the
	// wrong password. Without it the response time alone enumerates accounts.
	decoyHash string
}

// NewService wires the service. It returns an error because the decoy hash is
// computed once here rather than per failed login.
func NewService(
	store *Store,
	tx *database.TxManager,
	hasher password.Hasher,
	cfg ServiceConfig,
	log *slog.Logger,
) (*Service, error) {
	if cfg.SessionTTL <= 0 {
		return nil, apperrors.Internal(
			errors.New("session ttl must be positive"), "configure auth service")
	}

	decoy, err := domain.GeneratePassword()
	if err != nil {
		return nil, err
	}
	decoyHash, err := hasher.Hash(decoy)
	if err != nil {
		return nil, apperrors.Internal(err, "prepare auth service")
	}

	return &Service{
		store:     store,
		tx:        tx,
		hasher:    hasher,
		cfg:       cfg,
		log:       log,
		now:       time.Now,
		decoyHash: decoyHash,
	}, nil
}

// WithClock replaces the clock. Tests only.
func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}

// LoginInput is a sign-in attempt.
type LoginInput struct {
	Email     string
	Password  string
	IP        string
	UserAgent string
}

// LoginResult carries what the handler needs to set the cookie and redirect.
type LoginResult struct {
	User    *domain.User
	Session *domain.Session
	// Token is the raw cookie value. It exists only for the length of this
	// response: it is never stored, never logged, and never returned again.
	Token string
}

// LogIn verifies a credential and starts a session.
//
// A new token is minted every time and nothing is carried over from a previous
// session, which is what makes session fixation a non-issue.
func (s *Service) LogIn(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := domain.NormaliseEmail(in.Email)

	user, err := s.store.UserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}

	// Verify against the decoy when there is no such account, so both paths do
	// the same Argon2id work. Returning early here would make an unknown address
	// measurably faster than a wrong password.
	if user == nil {
		_ = s.hasher.Verify(in.Password, s.decoyHash)
		return nil, ErrInvalidCredentials
	}

	if err := s.hasher.Verify(in.Password, user.PasswordHash); err != nil {
		if errors.Is(err, password.ErrMismatchedPassword) {
			return nil, ErrInvalidCredentials
		}
		// A malformed stored hash is our bug, not the user's, and must not be
		// reported as a wrong password -- that would hide it forever.
		return nil, apperrors.Internal(err, "verify password")
	}

	// Checked after the password, so it tells nothing to someone who does not
	// already hold the credential.
	if !user.CanSignIn() {
		return nil, ErrAccountSuspended
	}

	rawToken, tokenHash, err := mintToken()
	if err != nil {
		return nil, err
	}
	csrfToken, err := mintCSRFToken()
	if err != nil {
		return nil, err
	}

	session, err := domain.NewSession(
		idgenerator.NewUUIDv7(), user.UUID, tokenHash, csrfToken,
		in.IP, in.UserAgent, s.now(), s.cfg.SessionTTL,
	)
	if err != nil {
		return nil, err
	}
	if err := s.store.CreateSession(ctx, session); err != nil {
		return nil, err
	}

	s.log.InfoContext(ctx, "signed in",
		"user", user.UUID, "session", session.UUID, "ip", in.IP)

	return &LoginResult{User: user, Session: session, Token: rawToken}, nil
}

// Authenticate resolves a raw cookie value to its session and user.
//
// It distinguishes "no such session" from "session that has ended" so the caller
// can say something useful, and refuses a suspended account on every request
// rather than only at sign-in.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (*domain.User, *domain.Session, error) {
	if rawToken == "" {
		return nil, nil, ErrNoSession
	}

	session, err := s.store.SessionByTokenHash(ctx, hashToken(rawToken))
	if err != nil {
		return nil, nil, err
	}
	if session == nil {
		return nil, nil, ErrNoSession
	}
	if !session.IsUsable(s.now()) {
		return nil, nil, ErrSessionEnded
	}

	user, err := s.store.UserByUUID(ctx, session.UserUUID)
	if err != nil {
		return nil, nil, err
	}
	if user == nil {
		// The row cascades with the user, so this should be unreachable. Treat
		// it as an ended session rather than a 500: there is nothing the person
		// can do, and signing them out is the correct outcome.
		return nil, nil, ErrSessionEnded
	}
	if !user.CanSignIn() {
		return nil, nil, ErrAccountSuspended
	}

	return user, session, nil
}

// TouchIfStale advances last_used_at, but only when it is stale enough to be worth
// a write.
//
// Every authenticated request could bump it. That would turn every GET — including
// the board page, the most requested route there is — into a write on a database
// with exactly one writer. domain.TouchInterval is the throttle.
func (s *Service) TouchIfStale(ctx context.Context, session *domain.Session) error {
	now := s.now()
	if !session.NeedsTouch(now) {
		return nil
	}
	if err := s.store.TouchSession(ctx, session.UUID, now); err != nil {
		return err
	}
	session.LastUsedAt = now.UTC()
	return nil
}

// LogOut revokes one session. It is idempotent: a double-submitted sign-out must
// not show the user a failure.
func (s *Service) LogOut(ctx context.Context, sessionUUID string) error {
	return s.store.RevokeSession(ctx, sessionUUID, s.now())
}

// ChangeOwnPassword replaces a user's password after verifying the current one.
//
// keepSessionUUID is the session doing the change; every other session the user
// holds is revoked. Signing someone out of the browser they just secured their
// account in would be a hostile way to behave, but leaving other browsers signed
// in would defeat the point of changing it.
func (s *Service) ChangeOwnPassword(
	ctx context.Context,
	user *domain.User,
	current, next, keepSessionUUID string,
) error {
	if err := s.hasher.Verify(current, user.PasswordHash); err != nil {
		if errors.Is(err, password.ErrMismatchedPassword) {
			return apperrors.Validation("That is not your current password.").
				WithField("current_password", "is wrong")
		}
		return apperrors.Internal(err, "verify password")
	}
	if err := domain.ValidatePassword(next); err != nil {
		return err
	}
	if current == next {
		return ErrSamePassword
	}

	hash, err := s.hasher.Hash(next)
	if err != nil {
		return apperrors.Internal(err, "hash password")
	}

	now := s.now()
	// One transaction: the new password and the revocations land together, so
	// there is no instant in which the password has changed but old sessions are
	// still live.
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := user.SetPassword(hash, false, now); err != nil {
			return err
		}
		if err := s.store.UpdateUser(ctx, user); err != nil {
			return err
		}
		revoked, err := s.store.RevokeSessionsForUserExcept(ctx, user.UUID, keepSessionUUID, now)
		if err != nil {
			return err
		}
		s.log.InfoContext(ctx, "password changed",
			"user", user.UUID, "other_sessions_revoked", revoked)
		return nil
	})
}

// BootstrapResult reports what the first-boot check did.
type BootstrapResult struct {
	// Created is false when an account already existed and nothing was done.
	Created bool
	Email   string
	// GeneratedPassword is non-empty only when no password was configured, in
	// which case this is the single time it will ever be available.
	GeneratedPassword string
}

// EnsureSuperAdmin creates the operator's account if the installation has none.
//
// Runs at every boot and does nothing on all but the first. A configured password
// is used as given; with none, one is generated and returned for the caller to
// print exactly once. Either way must_change_password is set, so a password that
// reached a shell history or a compose file cannot remain in use.
func (s *Service) EnsureSuperAdmin(ctx context.Context, email, plaintext string) (BootstrapResult, error) {
	count, err := s.store.CountUsers(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	if count > 0 {
		return BootstrapResult{}, nil
	}

	email = domain.NormaliseEmail(email)
	if email == "" {
		return BootstrapResult{}, apperrors.Invalid(
			"SUPER_ADMIN_EMAIL must be set to create the first account")
	}

	generated := ""
	if plaintext == "" {
		generated, err = domain.GeneratePassword()
		if err != nil {
			return BootstrapResult{}, err
		}
		plaintext = generated
	}
	if err := domain.ValidatePassword(plaintext); err != nil {
		return BootstrapResult{}, err
	}

	hash, err := s.hasher.Hash(plaintext)
	if err != nil {
		return BootstrapResult{}, apperrors.Internal(err, "hash password")
	}

	now := s.now()
	user, err := domain.NewUser(
		idgenerator.NewUUIDv7(), email, "Owner", hash, domain.RoleSuperAdmin, now)
	if err != nil {
		return BootstrapResult{}, err
	}
	user.MustChangePassword = true

	if err := s.store.CreateUser(ctx, user); err != nil {
		return BootstrapResult{}, err
	}

	return BootstrapResult{Created: true, Email: email, GeneratedPassword: generated}, nil
}

// ReapExpiredSessions deletes rows that can no longer authenticate anything.
//
// Revoked-but-unexpired rows are kept, so "your session has ended" can still be
// distinguished from "no such session".
func (s *Service) ReapExpiredSessions(ctx context.Context) (int64, error) {
	return s.store.DeleteExpiredSessions(ctx, s.now())
}
