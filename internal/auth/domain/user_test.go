package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

var testNow = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

func TestNormaliseEmail(t *testing.T) {
	// The schema stores addresses folded and a CHECK enforces it, because
	// case-insensitive uniqueness has no portable DDL.
	tests := map[string]string{
		"Owner@Example.COM":  "owner@example.com",
		"  spaced@x.io  ":    "spaced@x.io",
		"already@lower.case": "already@lower.case",
		"":                   "",
	}
	for in, want := range tests {
		assert.Equal(t, want, NormaliseEmail(in), "input %q", in)
	}
}

func TestNewUser(t *testing.T) {
	u, err := NewUser("u-1", "  Owner@Example.com ", "  The Owner  ", "hash",
		RoleSuperAdmin, testNow)

	require.NoError(t, err)
	assert.Equal(t, "owner@example.com", u.Email, "folded on the way in")
	assert.Equal(t, "The Owner", u.DisplayName, "trimmed")
	assert.Equal(t, StatusActive, u.Status, "a new account can sign in")
	assert.True(t, u.CanSignIn())
	assert.True(t, u.IsSuperAdmin())
	assert.False(t, u.MustChangePassword)
	assert.Equal(t, testNow, u.CreatedAt)
}

func TestNewUserValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		email      string
		display    string
		hash       string
		role       Role
		wantFields []string
	}{
		{
			name:       "everything missing",
			role:       Role(""),
			wantFields: []string{"uuid", "email", "display_name", "password", "role"},
		},
		{
			name: "malformed address", uuid: "u", email: "not-an-address",
			display: "N", hash: "h", role: RoleMember,
			wantFields: []string{"email"},
		},
		{
			name: "address with a display part is not the address itself",
			uuid: "u", email: "the owner <owner@example.com>",
			display: "N", hash: "h", role: RoleMember,
			wantFields: []string{"email"},
		},
		{
			name: "address too long", uuid: "u",
			email:   strings.Repeat("a", MaxEmailLength) + "@example.com",
			display: "N", hash: "h", role: RoleMember,
			wantFields: []string{"email"},
		},
		{
			name: "display name too long", uuid: "u", email: "a@b.co",
			display: strings.Repeat("x", MaxDisplayNameLength+1), hash: "h", role: RoleMember,
			wantFields: []string{"display_name"},
		},
		{
			name: "unknown role", uuid: "u", email: "a@b.co",
			display: "N", hash: "h", role: Role("wizard"),
			wantFields: []string{"role"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u, err := NewUser(tc.uuid, tc.email, tc.display, tc.hash, tc.role, testNow)

			require.Error(t, err)
			assert.Nil(t, u)
			assert.ErrorIs(t, err, apperrors.New(apperrors.CodeValidation, ""))

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestUserMutations(t *testing.T) {
	later := testNow.Add(time.Hour)

	t.Run("rename", func(t *testing.T) {
		u, _ := NewUser("u", "a@b.co", "Before", "h", RoleMember, testNow)
		require.NoError(t, u.Rename("  After  ", later))
		assert.Equal(t, "After", u.DisplayName)
		assert.Equal(t, later, u.UpdatedAt)

		assert.Error(t, u.Rename("   ", later))
		assert.Equal(t, "After", u.DisplayName, "a rejected rename changes nothing")
	})

	t.Run("change email", func(t *testing.T) {
		u, _ := NewUser("u", "a@b.co", "N", "h", RoleMember, testNow)
		require.NoError(t, u.ChangeEmail(" NEW@Example.com ", later))
		assert.Equal(t, "new@example.com", u.Email)

		assert.Error(t, u.ChangeEmail("nope", later))
		assert.Equal(t, "new@example.com", u.Email)
	})

	t.Run("set password forces a change when the operator chose it", func(t *testing.T) {
		u, _ := NewUser("u", "a@b.co", "N", "h", RoleMember, testNow)

		require.NoError(t, u.SetPassword("handed-over", true, later))
		assert.Equal(t, "handed-over", u.PasswordHash)
		assert.True(t, u.MustChangePassword)

		require.NoError(t, u.SetPassword("their-own", false, later))
		assert.False(t, u.MustChangePassword, "a user changing their own is not forced again")

		assert.Error(t, u.SetPassword("", false, later))
	})

	t.Run("suspend and reinstate", func(t *testing.T) {
		u, _ := NewUser("u", "a@b.co", "N", "h", RoleMember, testNow)

		u.Suspend(later)
		assert.Equal(t, StatusSuspended, u.Status)
		assert.False(t, u.CanSignIn())

		u.Reinstate(later)
		assert.True(t, u.CanSignIn())
	})

	t.Run("promote and demote", func(t *testing.T) {
		u, _ := NewUser("u", "a@b.co", "N", "h", RoleMember, testNow)
		assert.False(t, u.IsSuperAdmin())

		u.Promote(later)
		assert.True(t, u.IsSuperAdmin())

		u.Demote(later)
		assert.False(t, u.IsSuperAdmin())
	})
}

func TestRoleAndStatusValidity(t *testing.T) {
	assert.True(t, RoleSuperAdmin.Valid())
	assert.True(t, RoleMember.Valid())
	assert.False(t, Role("").Valid())
	assert.False(t, Role("owner").Valid())

	assert.True(t, StatusActive.Valid())
	assert.True(t, StatusSuspended.Valid())
	assert.False(t, Status("banned").Valid())
}
