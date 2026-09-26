package auth

import (
	"context"
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Store reads and writes the users and sessions tables.
//
// Every query is written with named parameters rather than placeholders: ":uuid"
// is the same text on every dialect, while "?" and "$1" are not. sqlx rewrites
// them for whichever driver is registered, which is what lets this SQL move to
// PostgreSQL untouched.
type Store struct {
	tx *database.TxManager
}

// NewStore wires a store to the transaction manager.
func NewStore(tx *database.TxManager) *Store { return &Store{tx: tx} }

// userColumns is the projection, listed once. Writing it out rather than using
// SELECT * means adding a column cannot silently change what a scan produces.
const userColumns = `
	uuid, email, display_name, password_hash, role, status,
	must_change_password, created_at, updated_at`

// userModel is the row shape. It exists so the domain entity can hold plain
// time.Time while the column round-trips through database.Time — the driver will
// not do that conversion for a TIMESTAMPTZ column on its own.
type userModel struct {
	UUID               string        `db:"uuid"`
	Email              string        `db:"email"`
	DisplayName        string        `db:"display_name"`
	PasswordHash       string        `db:"password_hash"`
	Role               string        `db:"role"`
	Status             string        `db:"status"`
	MustChangePassword bool          `db:"must_change_password"`
	CreatedAt          database.Time `db:"created_at"`
	UpdatedAt          database.Time `db:"updated_at"`
}

func (m userModel) toDomain() *domain.User {
	return &domain.User{
		UUID:               m.UUID,
		Email:              m.Email,
		DisplayName:        m.DisplayName,
		PasswordHash:       m.PasswordHash,
		Role:               domain.Role(m.Role),
		Status:             domain.Status(m.Status),
		MustChangePassword: m.MustChangePassword,
		CreatedAt:          m.CreatedAt.Time(),
		UpdatedAt:          m.UpdatedAt.Time(),
	}
}

func toUserModel(u *domain.User) userModel {
	return userModel{
		UUID:               u.UUID,
		Email:              u.Email,
		DisplayName:        u.DisplayName,
		PasswordHash:       u.PasswordHash,
		Role:               string(u.Role),
		Status:             string(u.Status),
		MustChangePassword: u.MustChangePassword,
		CreatedAt:          database.NewTime(u.CreatedAt),
		UpdatedAt:          database.NewTime(u.UpdatedAt),
	}
}

// CreateUser inserts an account. A duplicate address is reported as a field
// error rather than a bare conflict, because the only thing the operator can do
// about it is pick another address.
func (s *Store) CreateUser(ctx context.Context, u *domain.User) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO users (`+userColumns+`)
		VALUES (:uuid, :email, :display_name, :password_hash, :role, :status,
		        :must_change_password, :created_at, :updated_at)`,
		toUserModel(u))
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("that email address is already in use").
				WithField("email", "is already in use").
				Wrap(err)
		}
		return database.MapError(err, "create user")
	}
	return nil
}

// UserByUUID returns the account, or (nil, nil) when there is none. Absence is
// not an error here: whether a missing user is a 404 or a redirect to sign-in is
// the caller's decision, not the store's.
func (s *Store) UserByUUID(ctx context.Context, uuid string) (*domain.User, error) {
	return s.oneUser(ctx, `SELECT `+userColumns+` FROM users WHERE uuid = :uuid`,
		map[string]any{"uuid": uuid})
}

// UserByEmail looks an account up by address. The argument is normalised first,
// so a caller that forgot to fold the case still gets a hit.
func (s *Store) UserByEmail(ctx context.Context, email string) (*domain.User, error) {
	return s.oneUser(ctx, `SELECT `+userColumns+` FROM users WHERE email = :email`,
		map[string]any{"email": domain.NormaliseEmail(email)})
}

func (s *Store) oneUser(ctx context.Context, query string, args map[string]any) (*domain.User, error) {
	var m userModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m, query, args)
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// ListUsers returns every account, newest last, for the admin console.
func (s *Store) ListUsers(ctx context.Context) ([]*domain.User, error) {
	var models []userModel
	if err := database.SelectNamed(ctx, s.tx.Reader(ctx), &models,
		`SELECT `+userColumns+` FROM users ORDER BY created_at, uuid`,
		map[string]any{}); err != nil {
		return nil, err
	}
	out := make([]*domain.User, 0, len(models))
	for _, m := range models {
		out = append(out, m.toDomain())
	}
	return out, nil
}

// CountUsers reports how many accounts exist. Boot uses it to decide whether to
// create the first super admin.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.tx.Reader(ctx).GetContext(ctx, &n, `SELECT count(*) FROM users`); err != nil {
		return 0, database.MapError(err, "count users")
	}
	return n, nil
}

// UpdateUser writes every mutable field back.
//
// It returns a not-found error when nothing matched, rather than succeeding
// silently: an update that hits no rows means the entity was deleted under the
// caller, and treating that as success loses the write with no trace.
func (s *Store) UpdateUser(ctx context.Context, u *domain.User) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE users SET
			email                = :email,
			display_name         = :display_name,
			password_hash        = :password_hash,
			role                 = :role,
			status               = :status,
			must_change_password = :must_change_password,
			updated_at           = :updated_at
		WHERE uuid = :uuid`,
		toUserModel(u))
	if err != nil {
		if database.IsUniqueViolation(err) {
			return apperrors.Conflict("that email address is already in use").
				WithField("email", "is already in use").
				Wrap(err)
		}
		return database.MapError(err, "update user")
	}
	return database.RequireRow(n, "that account no longer exists")
}

// DeleteUser removes an account. Sessions and project grants cascade; cards and
// comments they authored do not, because ON DELETE RESTRICT on those columns
// deliberately blocks a delete that would erase history.
func (s *Store) DeleteUser(ctx context.Context, uuid string) error {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
		`DELETE FROM users WHERE uuid = :uuid`, map[string]any{"uuid": uuid})
	if err != nil {
		if database.IsForeignKeyViolation(err) {
			return apperrors.Conflict(
				"that account still owns cards or comments; suspend it instead").Wrap(err)
		}
		return database.MapError(err, "delete user")
	}
	return database.RequireRow(n, "that account no longer exists")
}

const sessionColumns = `
	uuid, user_uuid, token_hash, csrf_token, ip, user_agent,
	created_at, last_used_at, expires_at, revoked_at`

type sessionModel struct {
	UUID       string            `db:"uuid"`
	UserUUID   string            `db:"user_uuid"`
	TokenHash  string            `db:"token_hash"`
	CSRFToken  string            `db:"csrf_token"`
	IP         string            `db:"ip"`
	UserAgent  string            `db:"user_agent"`
	CreatedAt  database.Time     `db:"created_at"`
	LastUsedAt database.Time     `db:"last_used_at"`
	ExpiresAt  database.Time     `db:"expires_at"`
	RevokedAt  database.NullTime `db:"revoked_at"`
}

func (m sessionModel) toDomain() *domain.Session {
	return &domain.Session{
		UUID:       m.UUID,
		UserUUID:   m.UserUUID,
		TokenHash:  m.TokenHash,
		CSRFToken:  m.CSRFToken,
		IP:         m.IP,
		UserAgent:  m.UserAgent,
		CreatedAt:  m.CreatedAt.Time(),
		LastUsedAt: m.LastUsedAt.Time(),
		ExpiresAt:  m.ExpiresAt.Time(),
		RevokedAt:  m.RevokedAt.Ptr(),
	}
}

func toSessionModel(s *domain.Session) sessionModel {
	return sessionModel{
		UUID:       s.UUID,
		UserUUID:   s.UserUUID,
		TokenHash:  s.TokenHash,
		CSRFToken:  s.CSRFToken,
		IP:         s.IP,
		UserAgent:  s.UserAgent,
		CreatedAt:  database.NewTime(s.CreatedAt),
		LastUsedAt: database.NewTime(s.LastUsedAt),
		ExpiresAt:  database.NewTime(s.ExpiresAt),
		RevokedAt:  database.NullTimeFrom(s.RevokedAt),
	}
}

// CreateSession records a sign-in.
func (s *Store) CreateSession(ctx context.Context, sess *domain.Session) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		INSERT INTO sessions (`+sessionColumns+`)
		VALUES (:uuid, :user_uuid, :token_hash, :csrf_token, :ip, :user_agent,
		        :created_at, :last_used_at, :expires_at, :revoked_at)`,
		toSessionModel(sess))
	return database.MapError(err, "create session")
}

// SessionByTokenHash looks a session up by the hash of its cookie, returning
// (nil, nil) when there is no such session. It deliberately does not filter out
// expired or revoked rows: the caller needs to distinguish "no session" from
// "session that has ended" to decide what to tell the user, and the domain's
// IsUsable answers that.
func (s *Store) SessionByTokenHash(ctx context.Context, tokenHash string) (*domain.Session, error) {
	var m sessionModel
	found, err := database.GetNamed(ctx, s.tx.Reader(ctx), &m,
		`SELECT `+sessionColumns+` FROM sessions WHERE token_hash = :token_hash`,
		map[string]any{"token_hash": tokenHash})
	if err != nil || !found {
		return nil, err
	}
	return m.toDomain(), nil
}

// TouchSession advances last_used_at. Called at most once a minute per session —
// see domain.TouchInterval for why that throttle exists.
func (s *Store) TouchSession(ctx context.Context, uuid string, at time.Time) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx,
		`UPDATE sessions SET last_used_at = :at WHERE uuid = :uuid`,
		map[string]any{"at": database.NewTime(at), "uuid": uuid})
	return database.MapError(err, "touch session")
}

// RevokeSession ends one session. Revoking an already-revoked or absent session
// is not an error: sign-out must be idempotent, because a double-submitted form
// should not show the user a failure.
func (s *Store) RevokeSession(ctx context.Context, uuid string, at time.Time) error {
	_, err := s.tx.Writer(ctx).NamedExecContext(ctx, `
		UPDATE sessions SET revoked_at = :at
		WHERE uuid = :uuid AND revoked_at IS NULL`,
		map[string]any{"at": database.NewNullTime(at), "uuid": uuid})
	return database.MapError(err, "revoke session")
}

// RevokeSessionsForUser ends every live session an account has, and reports how
// many. Suspending an account or resetting its password calls this; without it
// the account keeps working until its cookie expires.
func (s *Store) RevokeSessionsForUser(ctx context.Context, userUUID string, at time.Time) (int64, error) {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE sessions SET revoked_at = :at
		WHERE user_uuid = :user_uuid AND revoked_at IS NULL`,
		map[string]any{"at": database.NewNullTime(at), "user_uuid": userUUID})
	return n, database.MapError(err, "revoke sessions")
}

// RevokeSessionsForUserExcept ends every live session an account has apart from
// one, and reports how many.
//
// This is what a user changing their own password calls: every other browser is
// signed out, but the one they are typing in is not, because signing someone out
// for successfully securing their account is a hostile way to behave.
func (s *Store) RevokeSessionsForUserExcept(ctx context.Context, userUUID, exceptUUID string, at time.Time) (int64, error) {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx), `
		UPDATE sessions SET revoked_at = :at
		WHERE user_uuid = :user_uuid
		  AND uuid <> :except_uuid
		  AND revoked_at IS NULL`,
		map[string]any{
			"at": database.NewNullTime(at), "user_uuid": userUUID,
			"except_uuid": exceptUUID,
		})
	return n, database.MapError(err, "revoke sessions")
}

// DeleteExpiredSessions removes rows that can no longer authenticate anything,
// and reports how many went. Revoked rows are kept for a grace period so that
// "you were signed out" can still be explained.
func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) (int64, error) {
	n, err := database.ExecNamed(ctx, s.tx.Writer(ctx),
		`DELETE FROM sessions WHERE expires_at < :before`,
		map[string]any{"before": database.NewTime(before)})
	return n, database.MapError(err, "prune sessions")
}
