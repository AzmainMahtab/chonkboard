// Package authctx carries the signed-in user and their session on the request
// context.
//
// It depends on auth/domain and nothing else. That is a deliberate exception to
// "shared/ depends on no slice": identity is genuinely cross-cutting — every
// slice's handlers need the current user — and duplicating the entity here to
// avoid the import would mean two definitions of the same thing.
package authctx

import (
	"context"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
)

type userKey struct{}
type sessionKey struct{}

// WithUser returns a context carrying the signed-in user.
func WithUser(ctx context.Context, u *authdomain.User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// WithSession returns a context carrying the active session, which is what the
// CSRF check and sign-out need.
func WithSession(ctx context.Context, s *authdomain.Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

// User returns the signed-in user, or nil when the request is anonymous.
//
// It returns nil rather than an error because every caller is inside a route
// group that already required a session; the nil case is a programming mistake,
// not a runtime condition to branch on.
func User(ctx context.Context) *authdomain.User {
	u, _ := ctx.Value(userKey{}).(*authdomain.User)
	return u
}

// Session returns the active session, or nil when the request is anonymous.
func Session(ctx context.Context) *authdomain.Session {
	s, _ := ctx.Value(sessionKey{}).(*authdomain.Session)
	return s
}

// CSRFToken returns the token for the active session, or "" when anonymous.
func CSRFToken(ctx context.Context) string {
	if s := Session(ctx); s != nil {
		return s.CSRFToken
	}
	return ""
}
