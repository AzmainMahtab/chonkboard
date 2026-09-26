package auth_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
	"github.com/AzmainMahtab/chonkboard/internal/shared/password"
)

// serviceFixture wires a real store against a temp database, with the fake hasher.
// Real Argon2id is 64MB and ~50ms per call, which would make this file crawl.
type serviceFixture struct {
	svc   *auth.Service
	store *auth.Store
	ctx   context.Context
	now   time.Time
}

func newService(t *testing.T) *serviceFixture {
	t.Helper()
	_, tx := dbtest.NewWithTx(t)
	store := auth.NewStore(tx)

	svc, err := auth.NewService(store, tx, password.NewFake(),
		auth.ServiceConfig{SessionTTL: 24 * time.Hour}, dbtest.Discard())
	require.NoError(t, err)

	f := &serviceFixture{
		svc:   svc,
		store: store,
		ctx:   context.Background(),
		now:   time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
	}
	svc.WithClock(func() time.Time { return f.now })
	return f
}

// addUser creates an account whose password is the plaintext given.
func (f *serviceFixture) addUser(t *testing.T, email, plaintext string, role domain.Role) *domain.User {
	t.Helper()
	hash, err := password.NewFake().Hash(plaintext)
	require.NoError(t, err)

	u, err := domain.NewUser(idgenerator.NewUUIDv7(), email, "Test Person", hash, role, f.now)
	require.NoError(t, err)
	require.NoError(t, f.store.CreateUser(f.ctx, u))
	return u
}

func TestLogInSucceeds(t *testing.T) {
	f := newService(t)
	user := f.addUser(t, "person@example.com", "correct horse battery", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email:     "  Person@Example.COM  ", // folded and trimmed on the way in
		Password:  "correct horse battery",
		IP:        "203.0.113.9",
		UserAgent: "Firefox",
	})

	require.NoError(t, err)
	assert.Equal(t, user.UUID, result.User.UUID)
	assert.NotEmpty(t, result.Token)
	assert.Equal(t, f.now.Add(24*time.Hour), result.Session.ExpiresAt)
	assert.NotEmpty(t, result.Session.CSRFToken)
	assert.Equal(t, "203.0.113.9", result.Session.IP)
}

func TestTheRawTokenIsNeverStored(t *testing.T) {
	// A dump of the sessions table must yield hashes, not live sessions.
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	stored, err := f.store.SessionByTokenHash(f.ctx, result.Session.TokenHash)
	require.NoError(t, err)
	require.NotNil(t, stored)

	assert.NotEqual(t, result.Token, stored.TokenHash)
	assert.Len(t, stored.TokenHash, 64, "hex-encoded SHA-256")

	// And the raw token cannot be used as a lookup key directly.
	byRaw, err := f.store.SessionByTokenHash(f.ctx, result.Token)
	require.NoError(t, err)
	assert.Nil(t, byRaw)
}

func TestEveryLoginMintsAFreshToken(t *testing.T) {
	// Nothing is carried over from a previous session, which is what makes
	// session fixation a non-issue.
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	first, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)
	second, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	assert.NotEqual(t, first.Token, second.Token)
	assert.NotEqual(t, first.Session.UUID, second.Session.UUID)
	assert.NotEqual(t, first.Session.CSRFToken, second.Session.CSRFToken)
}

func TestLogInDoesNotDistinguishAnUnknownAddressFromAWrongPassword(t *testing.T) {
	// The whole of the account-enumeration defence. Same error value, same
	// message, and -- because the unknown-address path verifies against a decoy
	// hash -- the same work.
	f := newService(t)
	f.addUser(t, "known@example.com", "the right password", domain.RoleMember)

	unknown := f.svc
	_, errUnknown := unknown.LogIn(f.ctx, auth.LoginInput{
		Email: "nobody@example.com", Password: "the right password",
	})
	_, errWrongPassword := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "known@example.com", Password: "the wrong password",
	})

	require.Error(t, errUnknown)
	require.Error(t, errWrongPassword)
	assert.ErrorIs(t, errUnknown, auth.ErrInvalidCredentials)
	assert.ErrorIs(t, errWrongPassword, auth.ErrInvalidCredentials)
	assert.Equal(t,
		apperrors.From(errUnknown).Message,
		apperrors.From(errWrongPassword).Message,
		"the messages must be identical")
	assert.Equal(t, 401, apperrors.From(errUnknown).Status())
}

func TestASuspendedAccountCannotSignIn(t *testing.T) {
	f := newService(t)
	user := f.addUser(t, "suspended@example.com", "a good long password", domain.RoleMember)
	user.Suspend(f.now)
	require.NoError(t, f.store.UpdateUser(f.ctx, user))

	_, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "suspended@example.com", Password: "a good long password",
	})

	assert.ErrorIs(t, err, auth.ErrAccountSuspended)
}

func TestSuspensionIsCheckedAfterThePassword(t *testing.T) {
	// So the distinct "suspended" message tells nothing to somebody who does not
	// already hold the credential.
	f := newService(t)
	user := f.addUser(t, "suspended@example.com", "a good long password", domain.RoleMember)
	user.Suspend(f.now)
	require.NoError(t, f.store.UpdateUser(f.ctx, user))

	_, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "suspended@example.com", Password: "the wrong password",
	})

	assert.ErrorIs(t, err, auth.ErrInvalidCredentials,
		"a wrong password on a suspended account is still just a wrong password")
}

func TestAMalformedStoredHashIsAnInternalErrorNotAWrongPassword(t *testing.T) {
	// Reporting it as a wrong password would hide the bug forever while locking
	// the person out.
	f := newService(t)
	user := f.addUser(t, "broken@example.com", "a good long password", domain.RoleMember)
	user.PasswordHash = "not-a-hash-at-all"
	require.NoError(t, f.store.UpdateUser(f.ctx, user))

	_, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "broken@example.com", Password: "a good long password",
	})

	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrInvalidCredentials)
	assert.Equal(t, 500, apperrors.From(err).Status())
}

func TestAuthenticate(t *testing.T) {
	f := newService(t)
	user := f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	gotUser, gotSession, err := f.svc.Authenticate(f.ctx, result.Token)
	require.NoError(t, err)
	assert.Equal(t, user.UUID, gotUser.UUID)
	assert.Equal(t, result.Session.UUID, gotSession.UUID)
}

func TestAuthenticateRefusals(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, f *serviceFixture, token string)
		token   func(valid string) string
		wantErr error
	}{
		{
			name:    "no cookie at all",
			token:   func(string) string { return "" },
			wantErr: auth.ErrNoSession,
		},
		{
			name:    "a token that matches nothing",
			token:   func(string) string { return "not-a-real-token" },
			wantErr: auth.ErrNoSession,
		},
		{
			name: "a revoked session",
			arrange: func(t *testing.T, f *serviceFixture, _ string) {
				sessions, err := f.store.ListUsers(f.ctx)
				require.NoError(t, err)
				require.NotEmpty(t, sessions)
				_, err = f.store.RevokeSessionsForUser(f.ctx, sessions[0].UUID, f.now)
				require.NoError(t, err)
			},
			wantErr: auth.ErrSessionEnded,
		},
		{
			name: "an expired session",
			arrange: func(t *testing.T, f *serviceFixture, _ string) {
				f.now = f.now.Add(25 * time.Hour) // TTL is 24h
			},
			wantErr: auth.ErrSessionEnded,
		},
		{
			name: "the account was suspended after signing in",
			arrange: func(t *testing.T, f *serviceFixture, _ string) {
				user, err := f.store.UserByEmail(f.ctx, "person@example.com")
				require.NoError(t, err)
				user.Suspend(f.now)
				require.NoError(t, f.store.UpdateUser(f.ctx, user))
			},
			wantErr: auth.ErrAccountSuspended,
		},
		{
			name: "the account was deleted",
			arrange: func(t *testing.T, f *serviceFixture, _ string) {
				user, err := f.store.UserByEmail(f.ctx, "person@example.com")
				require.NoError(t, err)
				require.NoError(t, f.store.DeleteUser(f.ctx, user.UUID))
			},
			wantErr: auth.ErrSessionEnded,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newService(t)
			f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

			result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
				Email: "person@example.com", Password: "a good long password",
			})
			require.NoError(t, err)

			token := result.Token
			if tc.token != nil {
				token = tc.token(result.Token)
			}
			if tc.arrange != nil {
				tc.arrange(t, f, result.Token)
			}

			_, _, err = f.svc.Authenticate(f.ctx, token)
			assert.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestExpiryIsCheckedAtTheBoundary(t *testing.T) {
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	f.now = result.Session.ExpiresAt.Add(-time.Nanosecond)
	_, _, err = f.svc.Authenticate(f.ctx, result.Token)
	assert.NoError(t, err, "one nanosecond before expiry it still works")

	f.now = result.Session.ExpiresAt
	_, _, err = f.svc.Authenticate(f.ctx, result.Token)
	assert.ErrorIs(t, err, auth.ErrSessionEnded, "exactly at expiry it does not")
}

func TestTouchIfStaleThrottlesWrites(t *testing.T) {
	// Every authenticated request could bump last_used_at. That would make every
	// GET a write on a database with one writer.
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)
	original := result.Session.LastUsedAt

	require.NoError(t, f.svc.TouchIfStale(f.ctx, result.Session))
	assert.Equal(t, original, result.Session.LastUsedAt, "too soon to be worth a write")

	f.now = f.now.Add(domain.TouchInterval)
	require.NoError(t, f.svc.TouchIfStale(f.ctx, result.Session))
	assert.Equal(t, f.now, result.Session.LastUsedAt)

	persisted, err := f.store.SessionByTokenHash(f.ctx, result.Session.TokenHash)
	require.NoError(t, err)
	assert.Equal(t, f.now, persisted.LastUsedAt, "and it reached the database")
}

func TestLogOutIsIdempotent(t *testing.T) {
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	require.NoError(t, f.svc.LogOut(f.ctx, result.Session.UUID))
	_, _, err = f.svc.Authenticate(f.ctx, result.Token)
	assert.ErrorIs(t, err, auth.ErrSessionEnded)

	// A double-submitted sign-out must not show the user a failure.
	assert.NoError(t, f.svc.LogOut(f.ctx, result.Session.UUID))
	assert.NoError(t, f.svc.LogOut(f.ctx, idgenerator.NewUUIDv7()))
}

func TestChangeOwnPassword(t *testing.T) {
	f := newService(t)
	user := f.addUser(t, "person@example.com", "the old password", domain.RoleMember)
	user.MustChangePassword = true
	require.NoError(t, f.store.UpdateUser(f.ctx, user))

	keep, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "the old password",
	})
	require.NoError(t, err)
	other, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "the old password",
	})
	require.NoError(t, err)

	require.NoError(t, f.svc.ChangeOwnPassword(
		f.ctx, keep.User, "the old password", "a brand new password", keep.Session.UUID))

	t.Run("the new password works and the old one does not", func(t *testing.T) {
		_, err := f.svc.LogIn(f.ctx, auth.LoginInput{
			Email: "person@example.com", Password: "a brand new password",
		})
		assert.NoError(t, err)

		_, err = f.svc.LogIn(f.ctx, auth.LoginInput{
			Email: "person@example.com", Password: "the old password",
		})
		assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
	})

	t.Run("the forced-change flag is cleared", func(t *testing.T) {
		reloaded, err := f.store.UserByUUID(f.ctx, keep.User.UUID)
		require.NoError(t, err)
		assert.False(t, reloaded.MustChangePassword)
	})

	t.Run("this session survives but every other is revoked", func(t *testing.T) {
		_, _, err := f.svc.Authenticate(f.ctx, keep.Token)
		assert.NoError(t, err, "signing someone out of the browser they just secured is hostile")

		_, _, err = f.svc.Authenticate(f.ctx, other.Token)
		assert.ErrorIs(t, err, auth.ErrSessionEnded, "but other browsers must go")
	})
}

func TestChangeOwnPasswordRefusals(t *testing.T) {
	tests := []struct {
		name      string
		current   string
		next      string
		wantErr   apperrors.Code
		wantField string
	}{
		{
			name:    "the current password is wrong",
			current: "not the old password", next: "a brand new password",
			wantErr: apperrors.CodeValidation, wantField: "current_password",
		},
		{
			name:    "the new password is too short",
			current: "the old password", next: "short",
			wantErr: apperrors.CodeValidation, wantField: "password",
		},
		{
			name:    "the new password is empty",
			current: "the old password", next: "",
			wantErr: apperrors.CodeValidation, wantField: "password",
		},
		{
			name:    "the new password is the same as the old",
			current: "the old password", next: "the old password",
			wantErr: apperrors.CodeValidation, wantField: "password",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newService(t)
			f.addUser(t, "person@example.com", "the old password", domain.RoleMember)
			session, err := f.svc.LogIn(f.ctx, auth.LoginInput{
				Email: "person@example.com", Password: "the old password",
			})
			require.NoError(t, err)

			err = f.svc.ChangeOwnPassword(
				f.ctx, session.User, tc.current, tc.next, session.Session.UUID)

			require.Error(t, err)
			assert.Equal(t, tc.wantErr, apperrors.From(err).Code)
			fields := apperrors.From(err).Fields
			require.NotEmpty(t, fields)
			assert.Equal(t, tc.wantField, fields[0].Field)

			// The old password must still work: a refused change changes nothing.
			_, err = f.svc.LogIn(f.ctx, auth.LoginInput{
				Email: "person@example.com", Password: "the old password",
			})
			assert.NoError(t, err)
		})
	}
}

func TestEnsureSuperAdminOnAFreshInstallation(t *testing.T) {
	f := newService(t)

	result, err := f.svc.EnsureSuperAdmin(f.ctx, "  Owner@Example.COM ", "")

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Equal(t, "owner@example.com", result.Email, "folded on the way in")
	require.NotEmpty(t, result.GeneratedPassword, "a password must be generated when none is configured")
	assert.Len(t, result.GeneratedPassword, domain.GeneratedPasswordLength)

	user, err := f.store.UserByEmail(f.ctx, "owner@example.com")
	require.NoError(t, err)
	require.NotNil(t, user)
	assert.True(t, user.IsSuperAdmin())
	assert.True(t, user.MustChangePassword,
		"a password that reached a shell history must not stay in use")

	// And the generated password actually works.
	_, err = f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "owner@example.com", Password: result.GeneratedPassword,
	})
	assert.NoError(t, err)
}

func TestEnsureSuperAdminUsesAConfiguredPassword(t *testing.T) {
	f := newService(t)

	result, err := f.svc.EnsureSuperAdmin(f.ctx, "owner@example.com", "a configured password")

	require.NoError(t, err)
	assert.True(t, result.Created)
	assert.Empty(t, result.GeneratedPassword, "nothing to reveal when it was configured")

	_, err = f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "owner@example.com", Password: "a configured password",
	})
	assert.NoError(t, err)
}

func TestEnsureSuperAdminDoesNothingWhenAnAccountExists(t *testing.T) {
	// It runs at every boot, so this is the normal case.
	f := newService(t)
	f.addUser(t, "existing@example.com", "a good long password", domain.RoleMember)

	result, err := f.svc.EnsureSuperAdmin(f.ctx, "owner@example.com", "a configured password")

	require.NoError(t, err)
	assert.False(t, result.Created)

	absent, err := f.store.UserByEmail(f.ctx, "owner@example.com")
	require.NoError(t, err)
	assert.Nil(t, absent, "a second super admin must not appear on a later boot")
}

func TestEnsureSuperAdminRefusesBadConfiguration(t *testing.T) {
	t.Run("no email", func(t *testing.T) {
		f := newService(t)
		_, err := f.svc.EnsureSuperAdmin(f.ctx, "", "a configured password")
		assert.ErrorContains(t, err, "SUPER_ADMIN_EMAIL")
	})

	t.Run("a configured password that fails our own policy", func(t *testing.T) {
		f := newService(t)
		_, err := f.svc.EnsureSuperAdmin(f.ctx, "owner@example.com", "short")
		require.Error(t, err)
		assert.Equal(t, apperrors.CodeValidation, apperrors.From(err).Code)
	})
}

func TestReapExpiredSessions(t *testing.T) {
	f := newService(t)
	f.addUser(t, "person@example.com", "a good long password", domain.RoleMember)

	old, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	f.now = f.now.Add(25 * time.Hour)
	live, err := f.svc.LogIn(f.ctx, auth.LoginInput{
		Email: "person@example.com", Password: "a good long password",
	})
	require.NoError(t, err)

	reaped, err := f.svc.ReapExpiredSessions(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), reaped)

	gone, err := f.store.SessionByTokenHash(f.ctx, old.Session.TokenHash)
	require.NoError(t, err)
	assert.Nil(t, gone)

	_, _, err = f.svc.Authenticate(f.ctx, live.Token)
	assert.NoError(t, err)
}

func TestNewServiceRefusesANonPositiveTTL(t *testing.T) {
	_, tx := dbtest.NewWithTx(t)
	_, err := auth.NewService(auth.NewStore(tx), tx, password.NewFake(),
		auth.ServiceConfig{SessionTTL: 0}, slog.Default())
	assert.ErrorContains(t, err, "session ttl")
}
