// Package middleware holds the cross-cutting HTTP concerns. Ordering matters and
// is fixed in httpserver.Router; nothing here assumes it runs first or last.
package middleware

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// RequestLogger records one line per request with its correlation id. It skips
// static assets, which would otherwise bury every real request, and reports an
// SSE stream once when it ends rather than leaving it unlogged for hours.
func RequestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}
			started := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			level := slog.LevelInfo
			if ww.Status() >= 500 {
				level = slog.LevelError
			} else if ww.Status() >= 400 {
				level = slog.LevelWarn
			}
			log.LogAttrs(r.Context(), level, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("took", time.Since(started).Round(time.Microsecond)),
				slog.String("request_id", chimw.GetReqID(r.Context())),
			)
		})
	}
}

// SecurityHeaders applies the same policy to every response.
//
// 'unsafe-eval' is here for exactly one reason: Alpine compiles its inline
// expressions with new Function. Alpine's CSP build removes the need for it at
// the cost of banning inline expressions in markup — a worthwhile trade later,
// not a blocker now. Everything else is same-origin: no CDN, no inline script,
// no external font.
func SecurityHeaders(next http.Handler) http.Handler {
	const csp = "default-src 'self'; " +
		"script-src 'self' 'unsafe-eval'; " +
		"style-src 'self'; " +
		"img-src 'self' data:; " +
		"font-src 'self'; " +
		"connect-src 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'; " +
		"base-uri 'none'; " +
		"object-src 'none'"

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
