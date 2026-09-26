package middleware

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/AzmainMahtab/chonkboard/internal/shared/authctx"
)

// CSRFHeader is where the token is expected. HTMX sends it via hx-headers on
// <body>, and board.js sends it explicitly on its fetch.
const CSRFHeader = "X-CSRF-Token"

// CSRFField is the fallback form field, for a plain <form> with no JavaScript --
// the login form, and anything that must work if htmx fails to load.
const CSRFField = "csrf_token"

// VerifyCSRF rejects a state-changing request that does not carry the session's
// CSRF token.
//
// This is not optional here. Cookie auth plus HTMX POSTs is exactly the shape
// SameSite=Lax does not fully cover: Lax still sends the cookie on a top-level
// cross-site navigation, so a form-triggered POST from another origin is not
// reliably stopped by the cookie attribute alone.
//
// Must run after RequireSession, since the token it compares against lives on the
// session.
func VerifyCSRF(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isSafeMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}

			expected := authctx.CSRFToken(r.Context())
			if expected == "" {
				// No session means no token to compare. RequireSession should
				// already have redirected, so reaching here is a routing
				// mistake -- refuse rather than wave it through.
				log.WarnContext(r.Context(), "csrf check with no session",
					"method", r.Method, "path", r.URL.Path)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			if !validCSRF(r, expected) {
				log.WarnContext(r.Context(), "csrf token rejected",
					"method", r.Method, "path", r.URL.Path)
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// isSafeMethod reports whether the method is defined as non-mutating. These are
// exempt; anything else is checked.
func isSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// validCSRF compares the presented token in constant time.
//
// The header is checked first because that is how every request from this
// application arrives. The form field is the fallback for a plain form post.
func validCSRF(r *http.Request, expected string) bool {
	if presented := r.Header.Get(CSRFHeader); presented != "" {
		return constantTimeEqual(presented, expected)
	}

	// A multipart body needs ParseMultipartForm: ParseForm reads only
	// application/x-www-form-urlencoded, so on a file upload it finds no fields at
	// all and the token looks absent. That made every attachment upload a 403 —
	// the upload form is a plain <form> with no JavaScript, so a field is its only
	// way to present a token.
	//
	// Both calls cache, so the handler still reads the form and the file afterwards.
	// Safe to do here only because LimitBody has already capped the body.
	if isMultipart(r) {
		if err := r.ParseMultipartForm(csrfMultipartMemory); err != nil {
			return false
		}
		return constantTimeEqual(r.FormValue(CSRFField), expected)
	}

	// ParseForm reads and caches the body, so the handler can still read the
	// form afterwards. Without this, consuming the body here would empty it.
	if err := r.ParseForm(); err != nil {
		return false
	}
	return constantTimeEqual(r.PostForm.Get(CSRFField), expected)
}

// csrfMultipartMemory is how much of a multipart body is held in memory before the
// rest spills to a temporary file. Small: the bytes are on their way to disk anyway,
// and Go removes the temporary file when the request ends.
const csrfMultipartMemory = 1 << 20

// isMultipart reports whether the body is a multipart form.
func isMultipart(r *http.Request) bool {
	return strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data")
}

// constantTimeEqual compares without leaking how much of the token matched. A
// plain == returns on the first differing byte, which is measurable.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
