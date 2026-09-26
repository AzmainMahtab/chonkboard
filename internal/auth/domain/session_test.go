package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

func TestNewSession(t *testing.T) {
	s, err := NewSession("s-1", "u-1", "hash", "csrf", "203.0.113.9", "Firefox",
		testNow, 24*time.Hour)

	require.NoError(t, err)
	assert.Equal(t, testNow, s.CreatedAt)
	assert.Equal(t, testNow, s.LastUsedAt)
	assert.Equal(t, testNow.Add(24*time.Hour), s.ExpiresAt)
	assert.Nil(t, s.RevokedAt)
	assert.True(t, s.IsUsable(testNow))
}

func TestNewSessionTruncatesALongUserAgent(t *testing.T) {
	// A long user agent is not an attack worth a failed sign-in.
	s, err := NewSession("s", "u", "h", "c", "", strings.Repeat("x", MaxUserAgentLength*2),
		testNow, time.Hour)

	require.NoError(t, err)
	assert.Len(t, s.UserAgent, MaxUserAgentLength)
}

func TestNewSessionValidation(t *testing.T) {
	tests := []struct {
		name       string
		uuid       string
		user       string
		hash       string
		csrf       string
		ttl        time.Duration
		wantFields []string
	}{
		{
			name:       "everything missing",
			wantFields: []string{"uuid", "user_uuid", "token_hash", "csrf_token", "ttl"},
		},
		{
			name: "no csrf token", uuid: "s", user: "u", hash: "h", ttl: time.Hour,
			wantFields: []string{"csrf_token"},
		},
		{
			name: "zero ttl", uuid: "s", user: "u", hash: "h", csrf: "c", ttl: 0,
			wantFields: []string{"ttl"},
		},
		{
			name: "negative ttl", uuid: "s", user: "u", hash: "h", csrf: "c", ttl: -time.Hour,
			wantFields: []string{"ttl"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSession(tc.uuid, tc.user, tc.hash, tc.csrf, "", "", testNow, tc.ttl)

			require.Error(t, err)
			assert.Nil(t, s)

			var got []string
			for _, f := range apperrors.From(err).Fields {
				got = append(got, f.Field)
			}
			assert.ElementsMatch(t, tc.wantFields, got)
		})
	}
}

func TestSessionUsability(t *testing.T) {
	tests := []struct {
		name       string
		at         time.Time
		revoke     bool
		wantUsable bool
	}{
		{"well inside its life", testNow.Add(time.Minute), false, true},
		{"one nanosecond before expiry", testNow.Add(time.Hour - time.Nanosecond), false, true},
		{"exactly at expiry", testNow.Add(time.Hour), false, false},
		{"after expiry", testNow.Add(2 * time.Hour), false, false},
		{"revoked but not expired", testNow.Add(time.Minute), true, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSession("s", "u", "h", "c", "", "", testNow, time.Hour)
			require.NoError(t, err)
			if tc.revoke {
				s.Revoke(testNow)
			}
			assert.Equal(t, tc.wantUsable, s.IsUsable(tc.at))
		})
	}
}

func TestExpiryBoundaryIsExclusive(t *testing.T) {
	// A session expiring exactly now is expired. The alternative is a
	// one-nanosecond window in which a dead session still works.
	s, err := NewSession("s", "u", "h", "c", "", "", testNow, time.Hour)
	require.NoError(t, err)

	assert.False(t, s.IsExpired(s.ExpiresAt.Add(-time.Nanosecond)))
	assert.True(t, s.IsExpired(s.ExpiresAt))
}

func TestRevokeIsIdempotent(t *testing.T) {
	// Sign-out must be safe to repeat: a double-submitted form should not show a
	// failure, and the original revocation time is what audit wants.
	s, err := NewSession("s", "u", "h", "c", "", "", testNow, time.Hour)
	require.NoError(t, err)

	first := testNow.Add(time.Minute)
	s.Revoke(first)
	require.NotNil(t, s.RevokedAt)
	assert.Equal(t, first, *s.RevokedAt)

	s.Revoke(testNow.Add(time.Hour))
	assert.Equal(t, first, *s.RevokedAt, "the first revocation time stands")
}

func TestNeedsTouchThrottlesWrites(t *testing.T) {
	// Every authenticated request could bump last_used_at, but that would turn
	// every GET into a write on a database with one writer.
	s, err := NewSession("s", "u", "h", "c", "", "", testNow, time.Hour)
	require.NoError(t, err)

	assert.False(t, s.NeedsTouch(testNow))
	assert.False(t, s.NeedsTouch(testNow.Add(TouchInterval-time.Second)))
	assert.True(t, s.NeedsTouch(testNow.Add(TouchInterval)))
	assert.True(t, s.NeedsTouch(testNow.Add(time.Hour)))
}
