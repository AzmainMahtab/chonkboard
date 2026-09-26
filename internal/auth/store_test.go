package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	"github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/platform/database/dbtest"
	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
	"github.com/AzmainMahtab/chonkboard/internal/shared/idgenerator"
)

func newStore(t *testing.T) (*auth.Store, context.Context) {
	t.Helper()
	_, tx := dbtest.NewWithTx(t)
	return auth.NewStore(tx), context.Background()
}

func makeUser(t *testing.T, email string, role domain.Role) *domain.User {
	t.Helper()
	u, err := domain.NewUser(idgenerator.NewUUIDv7(), email, "Test Person",
		"argon2id$fake", role, time.Now())
	require.NoError(t, err)
	return u
}

func TestCreateAndLoadUser(t *testing.T) {
	store, ctx := newStore(t)
	want := makeUser(t, "owner@example.com", domain.RoleSuperAdmin)
	require.NoError(t, store.CreateUser(ctx, want))

	t.Run("by uuid", func(t *testing.T) {
		got, err := store.UserByUUID(ctx, want.UUID)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, want.Email, got.Email)
		assert.Equal(t, domain.RoleSuperAdmin, got.Role)
		assert.Equal(t, domain.StatusActive, got.Status)
		assert.False(t, got.MustChangePassword)
		assert.True(t, want.CreatedAt.Truncate(time.Microsecond).Equal(got.CreatedAt),
			"timestamps must survive the round trip")
	})

	t.Run("by email", func(t *testing.T) {
		got, err := store.UserByEmail(ctx, "owner@example.com")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, want.UUID, got.UUID)
	})

	t.Run("by email is case insensitive", func(t *testing.T) {
		// The schema stores addresses folded and a CHECK enforces it, so the
		// lookup normalises too. This is the portable stand-in for COLLATE
		// NOCASE and CITEXT.
		got, err := store.UserByEmail(ctx, "  OWNER@Example.COM ")
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, want.UUID, got.UUID)
	})
}

func TestAbsenceIsNotAnError(t *testing.T) {
	// The store's contract: (nil, nil) for a row that is not there. Whether
	// that is a 404 or a redirect is the caller's decision.
	store, ctx := newStore(t)

	got, err := store.UserByUUID(ctx, idgenerator.NewUUIDv7())
	assert.NoError(t, err)
	assert.Nil(t, got)

	got, err = store.UserByEmail(ctx, "nobody@example.com")
	assert.NoError(t, err)
	assert.Nil(t, got)

	sess, err := store.SessionByTokenHash(ctx, "no-such-hash")
	assert.NoError(t, err)
	assert.Nil(t, sess)
}

func TestDuplicateEmailIsAFieldError(t *testing.T) {
	store, ctx := newStore(t)
	require.NoError(t, store.CreateUser(ctx, makeUser(t, "taken@example.com", domain.RoleMember)))

	err := store.CreateUser(ctx, makeUser(t, "taken@example.com", domain.RoleMember))

	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeConflict, ""))
	appErr := apperrors.From(err)
	require.Len(t, appErr.Fields, 1)
	assert.Equal(t, "email", appErr.Fields[0].Field)
}

func TestUpdateUser(t *testing.T) {
	store, ctx := newStore(t)
	u := makeUser(t, "person@example.com", domain.RoleMember)
	require.NoError(t, store.CreateUser(ctx, u))

	now := time.Now()
	require.NoError(t, u.Rename("Renamed Person", now))
	require.NoError(t, u.SetPassword("argon2id$new", true, now))
	u.Suspend(now)
	u.Promote(now)
	require.NoError(t, store.UpdateUser(ctx, u))

	got, err := store.UserByUUID(ctx, u.UUID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Renamed Person", got.DisplayName)
	assert.Equal(t, "argon2id$new", got.PasswordHash)
	assert.True(t, got.MustChangePassword)
	assert.Equal(t, domain.StatusSuspended, got.Status)
	assert.False(t, got.CanSignIn())
	assert.True(t, got.IsSuperAdmin())
}

func TestUpdateAbsentUserIsNotFound(t *testing.T) {
	// An UPDATE that matches nothing means the row went away under the caller.
	// Reporting that as success would lose the write with no trace.
	store, ctx := newStore(t)
	u := makeUser(t, "ghost@example.com", domain.RoleMember)

	err := store.UpdateUser(ctx, u)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
}

func TestDeleteAbsentUserIsNotFound(t *testing.T) {
	store, ctx := newStore(t)
	err := store.DeleteUser(ctx, idgenerator.NewUUIDv7())
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeNotFound, ""))
}

func TestListAndCountUsers(t *testing.T) {
	store, ctx := newStore(t)

	n, err := store.CountUsers(ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "boot uses this to decide whether to create the first admin")

	for _, email := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		require.NoError(t, store.CreateUser(ctx, makeUser(t, email, domain.RoleMember)))
	}

	n, err = store.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, n)

	users, err := store.ListUsers(ctx)
	require.NoError(t, err)
	assert.Len(t, users, 3)
}

func makeSession(t *testing.T, userUUID, hash string, ttl time.Duration) *domain.Session {
	t.Helper()
	s, err := domain.NewSession(idgenerator.NewUUIDv7(), userUUID, hash, "csrf-token",
		"203.0.113.9", "Mozilla/5.0", time.Now(), ttl)
	require.NoError(t, err)
	return s
}

func TestSessionLifecycle(t *testing.T) {
	store, ctx := newStore(t)
	u := makeUser(t, "person@example.com", domain.RoleMember)
	require.NoError(t, store.CreateUser(ctx, u))

	sess := makeSession(t, u.UUID, "hash-one", time.Hour)
	require.NoError(t, store.CreateSession(ctx, sess))

	got, err := store.SessionByTokenHash(ctx, "hash-one")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, u.UUID, got.UserUUID)
	assert.Equal(t, "csrf-token", got.CSRFToken)
	assert.Nil(t, got.RevokedAt, "a new session is not revoked")
	assert.True(t, got.IsUsable(time.Now()))

	t.Run("touch advances last_used_at", func(t *testing.T) {
		later := time.Now().Add(2 * time.Minute)
		require.NoError(t, store.TouchSession(ctx, sess.UUID, later))

		touched, err := store.SessionByTokenHash(ctx, "hash-one")
		require.NoError(t, err)
		assert.True(t, touched.LastUsedAt.After(got.LastUsedAt))
	})

	t.Run("revoke ends it", func(t *testing.T) {
		require.NoError(t, store.RevokeSession(ctx, sess.UUID, time.Now()))

		revoked, err := store.SessionByTokenHash(ctx, "hash-one")
		require.NoError(t, err)
		require.NotNil(t, revoked, "the row stays so the reason can be explained")
		require.NotNil(t, revoked.RevokedAt)
		assert.True(t, revoked.IsRevoked())
		assert.False(t, revoked.IsUsable(time.Now()))
	})

	t.Run("revoking twice is not an error", func(t *testing.T) {
		// Sign-out has to be idempotent: a double-submitted form must not show
		// the user a failure.
		assert.NoError(t, store.RevokeSession(ctx, sess.UUID, time.Now()))
		assert.NoError(t, store.RevokeSession(ctx, idgenerator.NewUUIDv7(), time.Now()))
	})
}

func TestRevokeSessionsForUserEndsOnlyThatUsers(t *testing.T) {
	store, ctx := newStore(t)
	mine := makeUser(t, "mine@example.com", domain.RoleMember)
	theirs := makeUser(t, "theirs@example.com", domain.RoleMember)
	require.NoError(t, store.CreateUser(ctx, mine))
	require.NoError(t, store.CreateUser(ctx, theirs))

	require.NoError(t, store.CreateSession(ctx, makeSession(t, mine.UUID, "m1", time.Hour)))
	require.NoError(t, store.CreateSession(ctx, makeSession(t, mine.UUID, "m2", time.Hour)))
	require.NoError(t, store.CreateSession(ctx, makeSession(t, theirs.UUID, "t1", time.Hour)))

	n, err := store.RevokeSessionsForUser(ctx, mine.UUID, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(2), n, "both of this user's sessions")

	for _, hash := range []string{"m1", "m2"} {
		s, err := store.SessionByTokenHash(ctx, hash)
		require.NoError(t, err)
		assert.True(t, s.IsRevoked(), hash)
	}
	other, err := store.SessionByTokenHash(ctx, "t1")
	require.NoError(t, err)
	assert.False(t, other.IsRevoked(), "another user's session must survive")

	again, err := store.RevokeSessionsForUser(ctx, mine.UUID, time.Now())
	require.NoError(t, err)
	assert.Zero(t, again, "already-revoked sessions are not touched again")
}

func TestSessionForNobodyIsRefused(t *testing.T) {
	// Proves foreign keys are enforced through the store, not just in a raw
	// query: without the pragma this insert would succeed.
	store, ctx := newStore(t)

	err := store.CreateSession(ctx, makeSession(t, idgenerator.NewUUIDv7(), "orphan", time.Hour))
	require.Error(t, err)
	assert.ErrorIs(t, err, apperrors.New(apperrors.CodeInvalid, ""))
}

func TestDeletingAUserTakesTheirSessions(t *testing.T) {
	store, ctx := newStore(t)
	u := makeUser(t, "leaving@example.com", domain.RoleMember)
	require.NoError(t, store.CreateUser(ctx, u))
	require.NoError(t, store.CreateSession(ctx, makeSession(t, u.UUID, "gone", time.Hour)))

	require.NoError(t, store.DeleteUser(ctx, u.UUID))

	sess, err := store.SessionByTokenHash(ctx, "gone")
	require.NoError(t, err)
	assert.Nil(t, sess, "sessions cascade with their user")
}

func TestDeleteExpiredSessions(t *testing.T) {
	store, ctx := newStore(t)
	u := makeUser(t, "person@example.com", domain.RoleMember)
	require.NoError(t, store.CreateUser(ctx, u))

	expired, err := domain.NewSession(idgenerator.NewUUIDv7(), u.UUID, "old", "csrf",
		"", "", time.Now().Add(-48*time.Hour), time.Hour)
	require.NoError(t, err)
	require.NoError(t, store.CreateSession(ctx, expired))
	require.NoError(t, store.CreateSession(ctx, makeSession(t, u.UUID, "live", time.Hour)))

	n, err := store.DeleteExpiredSessions(ctx, time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	gone, err := store.SessionByTokenHash(ctx, "old")
	require.NoError(t, err)
	assert.Nil(t, gone)

	alive, err := store.SessionByTokenHash(ctx, "live")
	require.NoError(t, err)
	assert.NotNil(t, alive)
}
