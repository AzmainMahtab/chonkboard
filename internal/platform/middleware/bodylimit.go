package middleware

import (
	"net/http"
)

// LimitBody caps how much of a request body any handler can be made to read.
//
// Applied globally and before anything parses a body, which matters more than it
// looks: the CSRF check has to read a multipart form to find its token, so without a
// ceiling already in place an unbounded upload would be spooled to disk *before*
// anything had a chance to refuse it. One ceiling, established first, is what makes
// parsing safe further down.
//
// GET and HEAD are left alone — they carry no body worth reading, and the SSE stream
// is a GET that must not be wrapped.
func LimitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if maxBytes > 0 && r.Body != nil && !isSafeMethod(r.Method) {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}
