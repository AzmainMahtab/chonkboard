package middleware

import (
	"log/slog"
	"net/http"
	"strings"

	authdomain "github.com/AzmainMahtab/chonkboard/internal/auth/domain"
	"github.com/AzmainMahtab/chonkboard/internal/shared/ratelimit"
)

// LoginRateLimit throttles sign-in attempts by address and by account.
//
// Both keys matter and neither is sufficient. Per-IP alone lets a botnet spread one
// password across thousands of addresses; per-account alone lets one host walk a
// dictionary against many accounts.
//
// They get separate limiters because they are different risks. An IP is shared —
// an office behind one NAT is many legitimate people — so its allowance is wide.
// An email address is one person, so its allowance is tight, and that is the one
// that actually bounds a guessing attack on a given account.
func LoginRateLimit(byIP, byEmail *ratelimit.Limiter, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			// Cached, so the handler and the CSRF check still see the form.
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}

			// RealIP runs earlier in the stack, so RemoteAddr is the client
			// rather than the proxy -- provided the proxy sets X-Forwarded-For.
			// If it does not, every request shares one bucket, which fails
			// closed rather than open.
			ip := clientIP(r)
			email := authdomain.NormaliseEmail(r.PostForm.Get("email"))

			checks := []struct {
				kind    string
				key     string
				limiter *ratelimit.Limiter
			}{
				{"ip", ip, byIP},
			}
			if email != "" {
				checks = append(checks, struct {
					kind    string
					key     string
					limiter *ratelimit.Limiter
				}{"email", email, byEmail})
			}

			for _, c := range checks {
				if !c.limiter.Allow(c.key) {
					// The key itself is not logged for the email case: a log is
					// a file somebody else may read, and which addresses are
					// being guessed at is not information worth persisting.
					log.WarnContext(r.Context(), "login rate limited",
						"limited_by", c.kind, "ip", ip)
					w.Header().Set("Retry-After", "60")
					http.Error(w,
						"Too many sign-in attempts. Please wait a minute and try again.",
						http.StatusTooManyRequests)
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// clientIP strips the port that RemoteAddr carries.
func clientIP(r *http.Request) string {
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		// An IPv6 literal is bracketed, so only trim past the closing bracket.
		if !strings.Contains(addr, "]") || idx > strings.Index(addr, "]") {
			addr = addr[:idx]
		}
	}
	return strings.Trim(addr, "[]")
}
