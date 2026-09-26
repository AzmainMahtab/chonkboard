package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/AzmainMahtab/chonkboard/internal/auth"
	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
	"github.com/AzmainMahtab/chonkboard/web/view"
)

// SessionConfig is what the session middleware needs.
type SessionConfig struct {
	// Secure must match what the cookie was set with, or clearing it silently
	// fails in some browsers.
	Secure bool
	// LoginPath is where an unauthenticated request is sent.
	LoginPath string
	// PasswordPath is where a user with must_change_password is held.
	PasswordPath string
	// LogoutPath stays reachable even while a password change is being forced.
	LogoutPath string
}

// Authenticator is the slice of the auth service this middleware uses.
//
// Declared here, by the consumer, so the middleware depends on two methods rather
// than on the whole service — which is also what makes it testable with a stub.
type Authenticator interface {
	Authenticate(ctx context.Context, rawToken string) (*authdomain.User, *authdomain.Session, error)
	TouchIfStale(ctx context.Context, session *authdomain.Session) error
}

// RequireSession authenticates every request in its group and puts the user and
// session on the context.
//
// Applied per route group rather than globally, so /login, /static and /healthz are
// never wrapped and cannot accidentally require the thing they exist to provide.
func RequireSession(svc Authenticator, cfg SessionConfig, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := auth.SessionCookieValue(r)
			if token == "" {
				redirect(w, r, cfg.LoginPath)
				return
			}

			user, session, err := svc.Authenticate(r.Context(), token)
			if err != nil {
				// The cookie is useless either way, so clear it rather than let
				// the browser resend it on every subsequent request.
				auth.ClearSessionCookie(w, cfg.Secure)

				if errors.Is(err, auth.ErrAccountSuspended) {
					log.InfoContext(r.Context(), "suspended account refused",
						"path", r.URL.Path)
				}
				redirect(w, r, cfg.LoginPath)
				return
			}

			// Best-effort. A failed touch must not cost the user their request;
			// it only affects how recently the session appears to have been used.
			if err := svc.TouchIfStale(r.Context(), session); err != nil {
				log.WarnContext(r.Context(), "cannot update session last-used",
					"session", session.UUID, "error", err)
			}

			ctx := authctx.WithSession(authctx.WithUser(r.Context(), user), session)
			// Also under the view layer's own key, so a form fragment can render
			// its hidden field without the UI package importing a domain type.
			ctx = view.WithCSRF(ctx, session.CSRFToken)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequirePasswordChange holds a user with must_change_password on the password page
// until they have chosen their own.
//
// This is what makes the credential-handover flow safe: a password the operator
// chose, or one that reached a shell history or a compose file, cannot stay in use.
// It must run after RequireSession.
func RequirePasswordChange(cfg SessionConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := authctx.User(r.Context())
			if user == nil || !user.MustChangePassword {
				next.ServeHTTP(w, r)
				return
			}
			// The password page itself and signing out must stay reachable, or
			// the redirect is a loop with no way out of it.
			if r.URL.Path == cfg.PasswordPath || r.URL.Path == cfg.LogoutPath {
				next.ServeHTTP(w, r)
				return
			}
			redirect(w, r, cfg.PasswordPath)
		})
	}
}

// redirect sends the browser to a path, using the header HTMX understands when the
// request came from HTMX.
//
// An HTMX request expects a fragment. A 303 to it would be followed by the
// XMLHttpRequest and the whole login page would be swapped into whatever element
// asked — so the board would appear to dissolve into a login form. HX-Redirect
// tells htmx to navigate the window instead.
func redirect(w http.ResponseWriter, r *http.Request, path string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", path)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, path, http.StatusSeeOther)
}
