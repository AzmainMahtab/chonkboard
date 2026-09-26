package domain

import (
	"time"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Session is a signed-in browser.
//
// TokenHash is the SHA-256 of the cookie value and the raw token is never
// stored, so a dump of the sessions table does not hand over live sessions. The
// consequence is that a token can only ever be looked up, never listed back to
// the user.
type Session struct {
	UUID       string
	UserUUID   string
	TokenHash  string
	CSRFToken  string
	IP         string
	UserAgent  string
	CreatedAt  time.Time
	LastUsedAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

// MaxUserAgentLength truncates what a client claims rather than rejecting it. A
// long user agent is not an attack worth a failed sign-in.
const MaxUserAgentLength = 400

// NewSession builds a session. The caller supplies the hash and the CSRF token
// because generating them is a service concern that needs entropy.
func NewSession(
	uuid, userUUID, tokenHash, csrfToken, ip, userAgent string,
	now time.Time,
	ttl time.Duration,
) (*Session, error) {
	err := apperrors.Validation("that session is not valid")
	invalid := false

	if uuid == "" {
		err = err.WithField("uuid", "is required")
		invalid = true
	}
	if userUUID == "" {
		err = err.WithField("user_uuid", "is required")
		invalid = true
	}
	if tokenHash == "" {
		err = err.WithField("token_hash", "is required")
		invalid = true
	}
	if csrfToken == "" {
		err = err.WithField("csrf_token", "is required")
		invalid = true
	}
	if ttl <= 0 {
		err = err.WithField("ttl", "must be positive")
		invalid = true
	}
	if invalid {
		return nil, err
	}

	if len(userAgent) > MaxUserAgentLength {
		userAgent = userAgent[:MaxUserAgentLength]
	}

	now = now.UTC()
	return &Session{
		UUID:       uuid,
		UserUUID:   userUUID,
		TokenHash:  tokenHash,
		CSRFToken:  csrfToken,
		IP:         ip,
		UserAgent:  userAgent,
		CreatedAt:  now,
		LastUsedAt: now,
		ExpiresAt:  now.Add(ttl),
	}, nil
}

// IsRevoked reports whether the session was ended deliberately.
func (s *Session) IsRevoked() bool { return s.RevokedAt != nil }

// IsExpired reports whether the session has aged out. The boundary is exclusive:
// a session expiring exactly now is expired, because the alternative is a
// one-nanosecond window in which a dead session works.
func (s *Session) IsExpired(now time.Time) bool { return !now.UTC().Before(s.ExpiresAt) }

// IsUsable reports whether the session may authenticate a request.
func (s *Session) IsUsable(now time.Time) bool { return !s.IsRevoked() && !s.IsExpired(now) }

// Revoke ends the session.
func (s *Session) Revoke(now time.Time) {
	if s.RevokedAt == nil {
		at := now.UTC()
		s.RevokedAt = &at
	}
}

// TouchInterval is how stale LastUsedAt is allowed to get.
//
// Every authenticated request could update it, but that would turn every GET
// into a write on a database with one writer — including the board page, which
// is the most requested route there is. Once a minute is enough to tell a live
// session from an abandoned one.
const TouchInterval = time.Minute

// NeedsTouch reports whether LastUsedAt is stale enough to be worth a write.
func (s *Session) NeedsTouch(now time.Time) bool {
	return now.UTC().Sub(s.LastUsedAt) >= TouchInterval
}
